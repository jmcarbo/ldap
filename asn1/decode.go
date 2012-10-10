package asn1

import (
	"bytes"
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

func (dec *Decoder) Decode(out interface{}) error {
	v := reflect.ValueOf(out).Elem()
	return dec.decodeField(v)
}

var (
	rawValueType  = reflect.TypeOf(RawValue{})
	EOC = fmt.Errorf("End-Of-Content")
)

func (dec *Decoder) decodeField(v reflect.Value) (err error) {
	class, tag, constructed, err := dec.decodeType()
	if err != nil {
		return
	}

	if class == 0x00 && tag == 0x00 {
		_, err = dec.r.Read(dec.buf[:1])
		if err != nil {
			return err
		} else if l := dec.buf[0]; l != 0x00 {
			return SyntaxError{fmt.Sprintf("End-Of-Content tag with non-zero length byte %#x", l)}
		}
		return EOC
	}

	if v.Type() == rawValueType {
		raw := RawValue{Class:class, Tag:tag, Constructed:constructed}
		raw.Bytes, err = dec.decodeLengthAndContent()
		if err != nil {
			return
		}
		v.Set(reflect.ValueOf(raw))
		return
	}

	err = checkTag(class, tag, constructed, v)
	if err != nil {
		return
	}

	if constructed {
		return dec.decodeConstructed(v)
	}
	return dec.decodePrimitive(v)
}

func (dec *Decoder) decodeConstructed(v reflect.Value) (err error) {
	switch v.Kind() {
	case reflect.Slice:
		return dec.decodeSequenceSlice(v)
	}
	return StructuralError{fmt.Sprintf("Unsupported Type: %v", v.Type())}
}

func (dec *Decoder) decodeSequenceSlice(v reflect.Value) (err error) {
	length, indefinite, err := dec.decodeLength()
	if err != nil {
		return
	}

	if !indefinite {
		b, err := dec.decodeContent(length, indefinite)
		if err != nil {
			return err
		}
		defer func(r io.Reader) {
			dec.r = r
		}(dec.r)
		dec.r = bytes.NewReader(b)
	}

	t := v.Type().Elem()
	v.Set(reflect.MakeSlice(v.Type(), 0, 0))
	for ok := true; ok; {
		vv := reflect.New(t).Elem()
		err = dec.decodeField(vv)
		if err == EOC || err == io.EOF {
			err = nil
			break
		} else if err != nil {
			return
		}
		v.Set(reflect.Append(v, vv))
	}
	return
}

func (dec *Decoder) decodePrimitive(v reflect.Value) (err error) {
	b, err := dec.decodeLengthAndContent()
	if err != nil {
		return
	}
	switch v.Kind() {
	case reflect.Slice:
		if v.Type().Elem().Kind() == reflect.Uint8 {
			return decodeByteSlice(b, v)
		}
	case reflect.Bool:
		return decodeBool(b, v)
	case reflect.Int64, reflect.Int32, reflect.Int16, reflect.Int8, reflect.Int:
		return decodeInteger(b, v)
	}
	return StructuralError{fmt.Sprintf("Unsupported Type: %v", v.Type())}
}

func (dec *Decoder) decodeType() (class, tag int, constructed bool, err error) {
	_, err = dec.r.Read(dec.buf[0:1])
	if err != nil {
		return
	}

	class = int(dec.buf[0] >> 6)
	constructed = dec.buf[0]&0x20 == 0x20

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

func (dec *Decoder) decodeLengthAndContent() (b []byte, err error) {
	length, indefinite, err := dec.decodeLength()
	if err != nil {
		return
	}
	return dec.decodeContent(length, indefinite)
}

func (dec *Decoder) decodeContent(length int, indefinite bool) (b []byte, err error) {
	if indefinite {
		b = make([]byte, 2)
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
	} else {
		b = make([]byte, length)
		_, err = io.ReadFull(dec.r, b)
		if err != nil {
			return
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

func checkTag(class, tag int, constructed bool, v reflect.Value) (err error) {
	var ok bool

	switch class {
	case ClassUniversal:
		switch tag {
		case TagBoolean:
			ok = !constructed && v.Kind() == reflect.Bool
		case TagOctetString:
			ok = !constructed && v.Kind() == reflect.Slice && v.Type().Elem().Kind() == reflect.Uint8
		case TagInteger, TagEnumerated:
			ok = !constructed && reflect.Int <= v.Kind() && v.Kind() <= reflect.Int64
		case TagSequence:
			ok = constructed && v.Kind() == reflect.Slice
		}
	}

	if !ok {
		err = StructuralError{
			fmt.Sprintf("tag mismatch (class = %#x, tag = %#x, constructed = %t, type = %v)",
				class, tag, constructed, v.Type())}
	}

	return
}

func decodeBool(b []byte, v reflect.Value) error {
	if len(b) != 1 {
		return SyntaxError{fmt.Sprintf("booleans must be only one byte (len = %d)", len(b))}
	}
	v.SetBool(b[0] != 0)
	return nil
}

func decodeByteSlice(b []byte, v reflect.Value) (err error) {
	v.SetBytes(b)
	return
}

func decodeInteger(b []byte, v reflect.Value) error {
	if len(b) == 0 {
		return SyntaxError{"integer must have at least one byte of content"}
	}

	var i int64
	for _, b := range b {
		i = i<<8 + int64(b)
	}

	if v.OverflowInt(i) {
		return StructuralError{"integer overflow"}
	}

	v.SetInt(i)

	return nil
}
