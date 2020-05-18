package openapi

import (
	"encoding/json"
	"io/ioutil"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/danielgtaylor/openapi-cli-generator/shorthand"
	"github.com/danielgtaylor/restish/cli"
	"github.com/getkin/kin-openapi/openapi3"
	"github.com/gosimple/slug"
	"github.com/spf13/cobra"

	"github.com/pquerna/cachecontrol"
)

// OpenAPI Extensions
const (
	// Change the CLI name for an operation or parameter
	ExtName = "x-cli-name"

	// Set additional command aliases for an operation
	ExtAliases = "x-cli-aliases"

	// Change the description of an operation or parameter
	ExtDescription = "x-cli-description"

	// Ignore a path, operation, or parameter
	ExtIgnore = "x-cli-ignore"

	// Create a hidden command for an operation. It will not show in the help,
	// but can still be called.
	ExtHidden = "x-cli-hidden"
)

// Resolver is able to resolve relative URIs against a base.
type Resolver interface {
	Resolve(uri string) (*url.URL, error)
}

// extStr returns the string value of an OpenAPI extension stored as a JSON
// raw message.
func extStr(v openapi3.ExtensionProps, key string) (decoded string) {
	i := v.Extensions[key]
	if i != nil {
		if err := json.Unmarshal(i.(json.RawMessage), &decoded); err != nil {
			cli.LogWarning("Cannot read extensions property %s", key)
			decoded = ""
		}
	}

	return
}

func getRequestInfo(op *openapi3.Operation) (string, *openapi3.Schema, []interface{}) {
	mts := make(map[string][]interface{})

	if op.RequestBody != nil && op.RequestBody.Value != nil {
		for mt, item := range op.RequestBody.Value.Content {
			var schema *openapi3.Schema
			var examples []interface{}

			if item.Schema != nil && item.Schema.Value != nil {
				schema = item.Schema.Value
			}

			if item.Example != nil {
				examples = append(examples, item.Example)
			} else {
				for _, ex := range item.Examples {
					if ex.Value != nil {
						examples = append(examples, ex.Value.Value)
						break
					}
				}
			}

			mts[mt] = []interface{}{schema, examples}
		}
	}

	// Prefer JSON.
	for mt, item := range mts {
		if strings.Contains(mt, "json") {
			return mt, item[0].(*openapi3.Schema), item[1].([]interface{})
		}
	}

	// Fall back to YAML next.
	for mt, item := range mts {
		if strings.Contains(mt, "yaml") {
			return mt, item[0].(*openapi3.Schema), item[1].([]interface{})
		}
	}

	// Last resort: return the first we find!
	for mt, item := range mts {
		return mt, item[0].(*openapi3.Schema), item[1].([]interface{})
	}

	return "", nil, nil
}

func openapiOperation(cmd *cobra.Command, method string, uriTemplate *url.URL, path *openapi3.PathItem, op *openapi3.Operation) cli.Operation {
	pathParams := []*cli.Param{}
	queryParams := []*cli.Param{}
	headerParams := []*cli.Param{}

	combinedParams := append(path.Parameters, op.Parameters...)

	for _, p := range combinedParams {
		if p.Value != nil {
			var def interface{}
			var example interface{}

			typ := "string"
			if p.Value.Schema != nil && p.Value.Schema.Value != nil {
				typ = p.Value.Schema.Value.Type

				if typ == "array" {
					// TODO: nil checks
					typ += "[" + p.Value.Schema.Value.Items.Value.Type + "]"
				}

				def = p.Value.Schema.Value.Default
				example = p.Value.Schema.Value.Example
			}

			if p.Value.Example != nil {
				example = p.Value.Example
			}

			style := cli.StyleSimple
			if p.Value.Style == "form" {
				style = cli.StyleForm
			}

			explode := false
			if p.Value.Explode != nil {
				explode = *p.Value.Explode
			}

			displayName := ""
			if override := extStr(p.Value.ExtensionProps, ExtName); override != "" {
				displayName = override
			}

			param := &cli.Param{
				Type:        typ,
				Name:        p.Value.Name,
				DisplayName: displayName,
				Description: p.Value.Description,
				Style:       style,
				Explode:     explode,
				Default:     def,
				Example:     example,
			}

			switch p.Value.In {
			case "path":
				pathParams = append(pathParams, param)
			case "query":
				queryParams = append(queryParams, param)
			case "header":
				headerParams = append(headerParams, param)
			}
		}
	}

	name := slug.Make(op.OperationID)
	if override := extStr(op.ExtensionProps, ExtName); override != "" {
		name = override
	}

	var aliases []string
	if op.Extensions[ExtAliases] != nil {
		// We need to decode the raw extension value into our string slice.
		json.Unmarshal(op.Extensions[ExtAliases].(json.RawMessage), &aliases)
	}

	desc := op.Description
	if override := extStr(op.ExtensionProps, ExtDescription); override != "" {
		desc = override
	}

	hidden := false
	if path.Extensions[ExtHidden] != nil {
		json.Unmarshal(path.Extensions[ExtHidden].(json.RawMessage), &hidden)
	}

	mediaType := ""
	if op.RequestBody != nil && op.RequestBody.Value != nil {
		mt, reqSchema, reqExamples := getRequestInfo(op)
		mediaType = mt

		var examples []string
		if len(reqExamples) > 0 {
			wroteHeader := false
			for _, ex := range reqExamples {
				if _, ok := ex.(string); !ok {
					// Not a string, so it's structured data. Let's marshal it to the
					// shorthand syntax if we can.
					if m, ok := ex.(map[string]interface{}); ok {
						ex = shorthand.Get(m)
						examples = append(examples, ex.(string))
						continue
					}

					b, _ := json.Marshal(ex)

					if !wroteHeader {
						desc += "\n## Input Example\n\n"
						wroteHeader = true
					}

					desc += "\n" + string(b) + "\n"
					continue
				}

				if !wroteHeader {
					desc += "\n## Input Example\n\n"
					wroteHeader = true
				}

				desc += "\n" + ex.(string) + "\n"
			}
		}

		if reqSchema != nil {
			desc += "\n## Request Schema (" + mt + ")\n\n```schema\n" + renderSchema(reqSchema, "", modeWrite) + "\n```\n"
		}
	}

	codes := []string{}
	for code := range op.Responses {
		codes = append(codes, code)
	}
	sort.Strings(codes)

	for _, code := range codes {
		if op.Responses[code] == nil || op.Responses[code].Value == nil {
			continue
		}

		resp := op.Responses[code].Value

		if len(resp.Content) > 0 {
			for ct, typeInfo := range resp.Content {
				desc += "\n## Response " + code + " (" + ct + ")\n"

				if typeInfo.Schema != nil && typeInfo.Schema.Value != nil {
					desc += "\n```schema\n" + renderSchema(typeInfo.Schema.Value, "", modeRead) + "\n```\n"
				}
			}
		} else {
			desc += "\n## Response " + code + "\n"
			if resp.Description != nil && *resp.Description != "" {
				desc += "\n" + *resp.Description + "\n"
			}
		}
	}

	return cli.Operation{
		Name:          name,
		Aliases:       aliases,
		Short:         op.Summary,
		Long:          desc,
		Method:        method,
		URITemplate:   uriTemplate.String(),
		PathParams:    pathParams,
		QueryParams:   queryParams,
		HeaderParams:  headerParams,
		BodyMediaType: mediaType,
		Hidden:        hidden,
	}
}

func loadOpenAPI3(cfg Resolver, cmd *cobra.Command, location *url.URL, resp *http.Response) (cli.API, error) {
	loader := openapi3.NewSwaggerLoader()

	data, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return cli.API{}, err
	}

	swagger, err := loader.LoadSwaggerFromDataWithPath(data, location)
	if err != nil {
		return cli.API{}, err
	}
	// spew.Dump(swagger)

	// See if this server has any base path prefix we need to account for.
	// TODO: handle variables in the server path?
	basePath := ""
	prefix := location.Scheme + "://" + location.Host
	for _, s := range swagger.Servers {
		if strings.HasPrefix(s.URL, prefix) {
			base, err := url.Parse(s.URL)
			if err != nil {
				return cli.API{}, err
			}
			basePath = base.Path
		}
	}

	operations := []cli.Operation{}
	for uri, path := range swagger.Paths {
		var ignore bool
		if path.Extensions[ExtIgnore] != nil {
			json.Unmarshal(path.Extensions[ExtIgnore].(json.RawMessage), &ignore)
		}
		if ignore {
			// Ignore this path.
			continue
		}

		resolved, err := cfg.Resolve(basePath + uri)
		if err != nil {
			return cli.API{}, err
		}

		for method, operation := range path.Operations() {
			if path.Extensions[ExtIgnore] != nil {
				json.Unmarshal(path.Extensions[ExtIgnore].(json.RawMessage), &ignore)
			}
			if ignore {
				// Ignore this operation.
				continue
			}

			operations = append(operations, openapiOperation(cmd, method, resolved, path, operation))
		}
	}

	short := ""
	long := ""
	if swagger.Info != nil {
		short = swagger.Info.Title
		long = swagger.Info.Description
	}

	// Assume we used an HTTP GET for getting the spec.
	req, _ := http.NewRequest(http.MethodGet, location.String(), nil)
	reasons, expires, _ := cachecontrol.CachableResponse(req, resp, cachecontrol.Options{})

	if len(reasons) == 0 && expires.IsZero() {
		// Default to one week.
		expires = time.Now().Add(7 * 24 * time.Hour)
	}

	api := cli.API{
		Short:      short,
		Long:       long,
		Operations: operations,
		CacheUntil: expires,
	}

	return api, nil
}

type loader struct {
	location *url.URL
	base     *url.URL
}

func (l *loader) Resolve(relURI string) (*url.URL, error) {
	parsed, err := url.Parse(relURI)
	if err != nil {
		return nil, err
	}

	return l.base.ResolveReference(parsed), nil
}

func (l *loader) LocationHints() []string {
	return []string{"/openapi.json", "/openapi.yaml"}
}

func (l *loader) Detect(resp *http.Response) bool {
	// Try to detect via header first
	if strings.HasPrefix(resp.Header.Get("content-type"), "application/vnd.oai.openapi") {
		return true
	}

	// Fall back to looking for the OpenAPI version in the body.
	body, _ := ioutil.ReadAll(resp.Body)
	defer resp.Body.Close()

	if strings.Contains(string(body), "openapi: 3") {
		return true
	}

	return false
}

func (l *loader) Load(entrypoint, spec url.URL, resp *http.Response) (cli.API, error) {
	l.location = &spec
	l.base = &entrypoint
	return loadOpenAPI3(l, cli.Root, &spec, resp)
}

// New creates a new OpenAPI loader.
func New() cli.Loader {
	return &loader{}
}
