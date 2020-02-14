package hashstructure

import (
	"encoding/binary"
	"fmt"
	"hash"
	"hash/fnv"
	"math"
	"reflect"
	"unsafe"
)

// ErrNotStringer is returned when there's an error with hash:"string"
type ErrNotStringer struct {
	Field string
}

// Error implements error for ErrNotStringer
func (ens *ErrNotStringer) Error() string {
	return fmt.Sprintf("hashstructure: %s has hash:\"string\" set, but does not implement fmt.Stringer", ens.Field)
}

// HashOptions are options that are available for hashing.
type HashOptions struct {
	// Hasher is the hash function to use. If this isn't set, it will
	// default to FNV.
	Hasher hash.Hash64

	// TagName is the struct tag to look at when hashing the structure.
	// By default this is "hash".
	TagName string

	// ZeroNil is flag determining if nil pointer should be treated equal
	// to a zero value of pointed type. By default this is false.
	ZeroNil bool
}

// Hash returns the hash value of an arbitrary value.
//
// If opts is nil, then default options will be used. See HashOptions
// for the default values. The same *HashOptions value cannot be used
// concurrently. None of the values within a *HashOptions struct are
// safe to read/write while hashing is being done.
//
// Notes on the value:
//
//   * Unexported fields on structs are ignored and do not affect the
//     hash value.
//
//   * Adding an exported field to a struct with the zero value will change
//     the hash value.
//
// For structs, the hashing can be controlled using tags. For example:
//
//    struct {
//        Name string
//        UUID string `hash:"ignore"`
//    }
//
// The available tag values are:
//
//   * "ignore" or "-" - The field will be ignored and not affect the hash code.
//
//   * "set" - The field will be treated as a set, where ordering doesn't
//             affect the hash code. This only works for slices.
//
//   * "string" - The field will be hashed as a string, only works when the
//                field implements fmt.Stringer
//
func Hash(v interface{}, opts *HashOptions) (uint64, error) {
	// Create default options
	if opts == nil {
		opts = &HashOptions{}
	}
	if opts.Hasher == nil {
		opts.Hasher = fnv.New64()
	}
	if opts.TagName == "" {
		opts.TagName = "hash"
	}

	// Reset the hash
	opts.Hasher.Reset()

	// Create our walker and walk the structure
	w := &walker{
		h:       opts.Hasher,
		tag:     opts.TagName,
		zeronil: opts.ZeroNil,
	}
	return w.visit(reflect.ValueOf(v), visitOpts{})
}

type walker struct {
	h       hash.Hash64
	tag     string
	zeronil bool
}

type visitOpts struct {
	// Flags are a bitmask of flags to affect behavior of this visit
	Flags visitFlag

	// Information about the struct containing this field
	Struct      interface{}
	StructField string
}

func (w *walker) visit(v reflect.Value, opts visitOpts) (uint64, error) {
	t := reflect.TypeOf(0)

	// Loop since these can be wrapped in multiple layers of pointers
	// and interfaces.
	for {
		// If we have an interface, dereference it. We have to do this up
		// here because it might be a nil in there and the check below must
		// catch that.
		if v.Kind() == reflect.Interface {
			v = v.Elem()
			continue
		}

		if v.Kind() == reflect.Ptr {
			if w.zeronil {
				t = v.Type().Elem()
			}
			v = reflect.Indirect(v)
			continue
		}

		break
	}

	// If it is nil, treat it like a zero.
	if !v.IsValid() {
		v = reflect.Zero(t)
	}

	k := v.Kind()

	// We can shortcut numeric values by directly binary writing them
	if k >= reflect.Bool && k <= reflect.Complex64 {
		// A direct hash calculation
		return hashNumber(w.h, v.Interface()), nil
	}

	switch k {
	case reflect.Array:
		var h uint64
		l := v.Len()
		for i := 0; i < l; i++ {
			current, err := w.visit(v.Index(i), visitOpts{})
			if err != nil {
				return 0, err
			}

			h = hashUpdateOrdered(w.h, h, current)
		}

		return h, nil

	case reflect.Map:
		var includeMap IncludableMap
		if opts.Struct != nil {
			if v, ok := opts.Struct.(IncludableMap); ok {
				includeMap = v
			}
		}

		// Build the hash for the map. We do this by XOR-ing all the key
		// and value hashes. This makes it deterministic despite ordering.
		var h uint64
		for _, k := range v.MapKeys() {
			v := v.MapIndex(k)
			if includeMap != nil {
				incl, err := includeMap.HashIncludeMap(
					opts.StructField, k.Interface(), v.Interface())
				if err != nil {
					return 0, err
				}
				if !incl {
					continue
				}
			}

			kh, err := w.visit(k, visitOpts{})
			if err != nil {
				return 0, err
			}
			vh, err := w.visit(v, visitOpts{})
			if err != nil {
				return 0, err
			}

			fieldHash := hashUpdateOrdered(w.h, kh, vh)
			h = hashUpdateUnordered(h, fieldHash)
		}

		return h, nil

	case reflect.Struct:
		parent := v.Interface()
		var include Includable
		if impl, ok := parent.(Includable); ok {
			include = impl
		}

		t := v.Type()
		h, err := w.visit(reflect.ValueOf(t.Name()), visitOpts{})
		if err != nil {
			return 0, err
		}

		l := v.NumField()
		for i := 0; i < l; i++ {
			if innerV := v.Field(i); v.CanSet() || t.Field(i).Name != "_" {
				var f visitFlag
				fieldType := t.Field(i)
				if fieldType.PkgPath != "" {
					// Unexported
					continue
				}

				tag := fieldType.Tag.Get(w.tag)
				if tag == "ignore" || tag == "-" {
					// Ignore this field
					continue
				}

				// if string is set, use the string value
				if tag == "string" {
					if impl, ok := innerV.Interface().(fmt.Stringer); ok {
						innerV = reflect.ValueOf(impl.String())
					} else {
						return 0, &ErrNotStringer{
							Field: v.Type().Field(i).Name,
						}
					}
				}

				// Check if we implement includable and check it
				if include != nil {
					incl, err := include.HashInclude(fieldType.Name, innerV)
					if err != nil {
						return 0, err
					}
					if !incl {
						continue
					}
				}

				switch tag {
				case "set":
					f |= visitFlagSet
				}

				kh, err := w.visit(reflect.ValueOf(fieldType.Name), visitOpts{})
				if err != nil {
					return 0, err
				}

				vh, err := w.visit(innerV, visitOpts{
					Flags:       f,
					Struct:      parent,
					StructField: fieldType.Name,
				})
				if err != nil {
					return 0, err
				}

				fieldHash := hashUpdateOrdered(w.h, kh, vh)
				h = hashUpdateUnordered(h, fieldHash)
			}
		}

		return h, nil

	case reflect.Slice:
		// We have two behaviors here. If it isn't a set, then we just
		// visit all the elements. If it is a set, then we do a deterministic
		// hash code.
		var h uint64
		set := (opts.Flags & visitFlagSet) != 0
		l := v.Len()
		for i := 0; i < l; i++ {
			current, err := w.visit(v.Index(i), visitOpts{})
			if err != nil {
				return 0, err
			}

			if set {
				h = hashUpdateUnordered(h, current)
			} else {
				h = hashUpdateOrdered(w.h, h, current)
			}
		}

		return h, nil

	case reflect.String:
		// Directly hash
		w.h.Reset()
		s := v.String()
		// avoid allocating a new byte slice for the string
		_, err := w.h.Write(*(*[]byte)(unsafe.Pointer(&s)))
		return w.h.Sum64(), err

	default:
		return 0, fmt.Errorf("unknown kind to hash: %s", k)
	}

}

func hashUpdateOrdered(h hash.Hash64, a, b uint64) uint64 {
	// For ordered updates, use a real hash function
	h.Reset()
	_, _ = h.Write([]byte{
		byte(a), byte(a >> 8), byte(a >> 16), byte(a >> 24), byte(a >> 32), byte(a >> 40), byte(a >> 48), byte(a >> 56),
		byte(b), byte(b >> 8), byte(b >> 16), byte(b >> 24), byte(b >> 32), byte(b >> 40), byte(b >> 48), byte(b >> 56),
	})
	return h.Sum64()
}

func hashUpdateUnordered(a, b uint64) uint64 {
	return a ^ b
}

func hashNumber(h hash.Hash64, i interface{}) uint64 {
	switch data := i.(type) {
	case bool:
		if data {
			return hash8(h, uint8(1))
		}
		return hash8(h, uint8(0))
	case int8:
		return hash8(h, uint8(data))
	case uint8:
		return hash8(h, data)

	case int16:
		return hash16(h, uint16(data))
	case uint16:
		return hash16(h, data)

	case int32:
		return hash32(h, uint32(data))
	case uint32:
		return hash32(h, data)
	case float32:
		return hash32(h, math.Float32bits(data))

	case int:
		return hash64(h, uint64(data))
	case int64:
		return hash64(h, uint64(data))
	case uint:
		return hash64(h, uint64(data))
	case uint64:
		return hash64(h, data)
	case uintptr:
		return hash64(h, uint64(data))
	case float64:
		return hash64(h, math.Float64bits(data))
	case complex64:
		return hash64(h, *(*uint64)(unsafe.Pointer(&data)))

	default:
		h.Reset()
		_ = binary.Write(h, binary.LittleEndian, i)
		return h.Sum64()
	}
}

func hash8(h hash.Hash64, i uint8) uint64 {
	h.Reset()
	_, _ = h.Write([]byte{i})
	return h.Sum64()
}

func hash16(h hash.Hash64, i uint16) uint64 {
	h.Reset()
	_, _ = h.Write([]byte{byte(i), byte(i >> 8)})
	return h.Sum64()
}

func hash32(h hash.Hash64, i uint32) uint64 {
	h.Reset()
	_, _ = h.Write([]byte{byte(i), byte(i >> 8), byte(i >> 16), byte(i >> 24)})
	return h.Sum64()
}

func hash64(h hash.Hash64, i uint64) uint64 {
	h.Reset()
	_, _ = h.Write([]byte{byte(i), byte(i >> 8), byte(i >> 16), byte(i >> 24), byte(i >> 32), byte(i >> 40), byte(i >> 48), byte(i >> 56)})
	return h.Sum64()
}

// visitFlag is used as a bitmask for affecting visit behavior
type visitFlag uint

const (
	_            visitFlag = iota
	visitFlagSet           = iota << 1
)
