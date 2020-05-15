package cli

import (
	"encoding/hex"
	"fmt"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"time"
)

// MarshalReadable marshals a value into a human-friendly readable format.
func MarshalReadable(v interface{}) ([]byte, error) {
	return marshalReadable("", v)
}

func marshalReadable(indent string, v interface{}) ([]byte, error) {
	rv := reflect.ValueOf(v)
	switch rv.Kind() {
	case reflect.Invalid:
		return []byte("null"), nil
	case reflect.Ptr:
		if rv.IsZero() {
			return []byte("null"), nil
		}

		return marshalReadable(indent, rv.Elem().Interface())
	case reflect.Bool:
		if v.(bool) == true {
			return []byte("true"), nil
		}

		return []byte("false"), nil
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64, reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		i := rv.Convert(reflect.TypeOf(int64(0))).Interface().(int64)
		return []byte(strconv.FormatInt(i, 10)), nil
	case reflect.Float32, reflect.Float64:
		f := rv.Convert(reflect.TypeOf(float64(0))).Interface().(float64)
		return []byte(strconv.FormatFloat(f, 'g', -1, 64)), nil
	case reflect.String:
		return []byte(`"` + strings.Replace(v.(string), `"`, `\"`, -1) + `"`), nil
	case reflect.Array:
		return marshalReadable(indent, rv.Slice(0, rv.Len()).Interface())
	case reflect.Slice:
		// Special case: empty slice should go in-line.
		if rv.Len() == 0 {
			return []byte("[]"), nil
		}

		// Detect binary []byte values and display the first few bytes as hex,
		// since that is easier to process in your head than base64.
		if binary, ok := v.([]byte); ok {
			suffix := ""
			if len(binary) > 10 {
				binary = binary[:10]
				suffix = "..."
			}
			return []byte("0x" + hex.EncodeToString(binary) + suffix), nil
		}

		// Otherwise, print out the slice.
		length := 0
		lines := []string{}
		for i := 0; i < rv.Len(); i++ {
			encoded, err := marshalReadable(indent+"  ", rv.Index(i).Interface())
			if err != nil {
				return nil, err
			}
			length += len(encoded) // TODO: handle multi-byte runes?
			lines = append(lines, string(encoded))
		}

		s := ""
		if len(indent)+(len(lines)*2)+length < 80 {
			// Special-case: short array gets inlined
			s += "[" + strings.Join(lines, ", ") + "]"
		} else {
			s += "[\n" + indent + "  " + strings.Join(lines, "\n  "+indent) + "\n" + indent + "]"
		}

		return []byte(s), nil
	case reflect.Map:
		// Special case: empty map should go in-line
		if rv.Len() == 0 {
			return []byte("{}"), nil
		}

		m := "{\n"

		// Sort the keys
		keys := rv.MapKeys()
		stringKeys := []string{}
		reverse := map[string]reflect.Value{}
		for _, k := range keys {
			ks := fmt.Sprintf("%v", k)
			stringKeys = append(stringKeys, ks)
			reverse[ks] = k
		}

		sort.Strings(stringKeys)

		// Write out each key/value pair.
		for _, k := range stringKeys {
			v := rv.MapIndex(reverse[k])
			encoded, err := marshalReadable(indent+"  ", v.Interface())
			if err != nil {
				return nil, err
			}
			m += indent + "  " + k + ": " + string(encoded) + "\n"
		}

		m += indent + "}"

		return []byte(m), nil
	case reflect.Struct:
		if t, ok := v.(time.Time); ok {
			return []byte(t.UTC().Format(time.RFC3339Nano)), nil
		}

		// TODO: user-defined structs, go through each field.
	}

	return nil, fmt.Errorf("unknown kind %s", rv.Kind())
}
