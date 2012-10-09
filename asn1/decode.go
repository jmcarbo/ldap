package asn1

import (
	"fmt"
	"io"
	"reflect"
)

type Decoder struct {
	r   io.Reader
	buf []byte
}

func NewDecoder(r io.Reader) *Decoder {
	return &Decoder{
		r: r,
		// 10 bytes ought to be long enough for any tag or length
		buf: make([]byte, 10),
	}
}

func (dec *Decoder) Decode(out interface{}) (err error) {
	v := reflect.ValueOf(out).Elem()
	raw, err := dec.decodeRawValue()
	if err != nil {
		return
	}

	if v.Type() == rawValueType {
		v.Set(reflect.ValueOf(raw))
		return
	}

	err = checkTag(raw.Class, raw.Tag, v)
	if err != nil {
		return
	}

	result, err := decodeValue(raw, v)
	if err == nil {
		v.Set(reflect.ValueOf(result))
	}
	return
}

func (dec *Decoder) decodeRawValue() (out RawValue, err error) {
	out.Class, out.Tag, out.IsConstructed, err = dec.decodeType()
	if err != nil {
		return
	}

	length, isIndefinite, err := dec.decodeLength()
	if err != nil {
		return
	}

	if isIndefinite {
		b := make([]byte, 2)
		_, err = io.ReadFull(dec.r, b)
		if err != nil {
			return
		}
		for {
			if b[len(b)-2] == 0 && b[len(b)-1] == 0 {
				b = b[:len(b)-2]
				break
			}
			if len(b) == cap(b) {
				bb := make([]byte, len(b), 2*len(b))
				copy(bb, b)
				b = bb
			}
			b = b[:len(b)+1]
			_, err = dec.r.Read(b[len(b)-1:])
			if err != nil {
				return
			}
		}
		out.Bytes = b
	} else {
		out.Bytes = make([]byte, length)
		_, err = io.ReadFull(dec.r, out.Bytes)
		if err != nil {
			return
		}
	}
	return
}

func (dec *Decoder) decodeType() (class, tag int, isCompound bool, err error) {
	_, err = dec.r.Read(dec.buf[0:1])
	if err != nil {
		return
	}

	class = int(dec.buf[0] >> 6)
	isCompound = dec.buf[0]&0x20 == 0x20

	if c := dec.buf[0] & 0x1f; c < 0x1f {
		tag = int(c)
	} else {
		_, err = dec.r.Read(dec.buf[0:1])
		if err != nil {
			return
		}

		if dec.buf[0]&0x7f == 0 {
			err = SyntaxError{"long-form tag"}
			return
		}

		for {
			tag = tag<<7 | int(dec.buf[0]&0x1f)

			if dec.buf[0]&0x80 == 0 {
				break
			}

			_, err = dec.r.Read(dec.buf[0:1])
			if err != nil {
				return
			}
		}
	}
	return
}

func (dec *Decoder) decodeLength() (length int, isIndefinite bool, err error) {
	_, err = dec.r.Read(dec.buf[0:1])
	if err != nil {
		return
	}

	if c := dec.buf[0]; c < 0x80 {
		length = int(c)
	} else if c == 0x80 {
		isIndefinite = true
	} else if c == 0xff {
		err = SyntaxError{"long-form length"}
		return
	} else {
		var width int
		n := c & 0x7f
		width, err = io.ReadFull(dec.r, dec.buf[0:n])
		if err != nil {
			return
		}
		for _, b := range dec.buf[0:width] {
			length = length<<8 | int(b)
		}
	}
	return
}

var (
	boolType      = reflect.TypeOf(true)
	byteSliceType = reflect.TypeOf([]byte{})
	intType       = reflect.TypeOf(int(0))
	int64Type     = reflect.TypeOf(int64(0))
	nullType      = reflect.TypeOf(Null{})
	rawValueType  = reflect.TypeOf(RawValue{})
)

func checkTag(class, tag int, v reflect.Value) (err error) {
	var ok bool

	switch class {
	case ClassUniversal:
		switch tag {
		case TagBoolean:
			ok = v.Kind() == reflect.Bool
		case TagOctetString:
			ok = v.Type() == byteSliceType
		case TagInteger:
			k := v.Kind()
			ok = k == reflect.Int || k == reflect.Int32 || k == reflect.Int64
		case TagNull:
			ok = v.Type() == nullType
		}
	}

	if !ok {
		err = StructuralError{
			fmt.Sprintf("tag mismatch (class = %#x, tag = %#x, type = %v)",
				class, tag, v.Type())}
	}

	return
}

func decodeValue(raw RawValue, v reflect.Value) (out interface{}, err error) {
	switch v.Type() {
	case boolType:
		out, err = decodeBool(raw)
	case byteSliceType:
		out, err = decodeByteSlice(raw)
	case intType:
		out, err = decodeInt(raw)
	case int64Type:
		out, err = decodeInt64(raw)
	case nullType:
		out, err = decodeNull(raw)
	default:
		err = StructuralError{fmt.Sprintf("Unsupported Type: %v", v.Type())}
	}
	return
}

func decodeBool(raw RawValue) (out interface{}, err error) {
	switch {
	case raw.IsConstructed:
		err = SyntaxError{"booleans must be primitive"}
	case len(raw.Bytes) != 1:
		err = SyntaxError{fmt.Sprintf("booleans must be only one byte (len = %d)", len(raw.Bytes))}
	default:
		out = raw.Bytes[0] != 0
	}
	return
}

func decodeByteSlice(raw RawValue) (out interface{}, err error) {
	switch {
	case raw.IsConstructed:
		err = SyntaxError{"constructed values are not supported"}
	default:
		b := make([]byte, len(raw.Bytes))
		copy(b, raw.Bytes)
		out = b
	}
	return
}

func decodeNull(raw RawValue) (out interface{}, err error) {
	switch {
	case raw.IsConstructed:
		err = SyntaxError{"null must be primitive"}
	case len(raw.Bytes) != 0:
		err = SyntaxError{fmt.Sprintf("null must not have content (len = %d)", len(raw.Bytes))}
	default:
		out = Null{}
	}
	return
}

func decodeInt64(raw RawValue) (out interface{}, err error) {
	switch {
	case raw.IsConstructed:
		err = SyntaxError{"integer must be primitive"}
	case len(raw.Bytes) == 0:
		err = SyntaxError{"integer must have at least one byte of content"}
	default:
		var i int64
		for _, b := range raw.Bytes {
			i = i<<8 + int64(b)
		}
		out = i
	}
	return
}

func decodeInt(raw RawValue) (out interface{}, err error) {
	ret, err := decodeInt64(raw)
	if err != nil {
		return
	}
	ret64 := ret.(int64)
	if i := int(ret64); ret64 == int64(i) {
		out = i
	} else {
		err = StructuralError{"integer overflow"}
	}
	return
}
