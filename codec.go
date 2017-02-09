// Copyright 2015 LinkedIn Corp. Licensed under the Apache License,
// Version 2.0 (the "License"); you may not use this file except in
// compliance with the License.  You may obtain a copy of the License
// at http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
//  distributed under the License is distributed on an "AS IS" BASIS,
//  WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or
// implied.Copyright [201X] LinkedIn Corp. Licensed under the Apache
// License, Version 2.0 (the "License"); you may not use this file
// except in compliance with the License.  You may obtain a copy of
// the License at http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
//  distributed under the License is distributed on an "AS IS" BASIS,
//  WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or
// implied.

// Package goavro is a library that encodes and decodes of Avro
// data. It provides an interface to encode data directly to io.Writer
// streams, and to decode data from io.Reader streams. Goavro fully
// adheres to version 1.7.7 of the Avro specification and data
// encoding.
package goavro

import (
	"encoding/json"
	"fmt"
	"io"
	"reflect"
	"strings"
	"bytes"
	"bufio"
)

const (
	mask = byte(127)
	flag = byte(128)
)

// ErrSchemaParse is returned when a Codec cannot be created due to an
// error while reading or parsing the schema.
type ErrSchemaParse struct {
	Message string
	Err     error
}

func (e ErrSchemaParse) Error() string {
	if e.Err == nil {
		return "cannot parse schema: " + e.Message
	}
	return "cannot parse schema: " + e.Message + ": " + e.Err.Error()
}

// ErrCodecBuild is returned when the encoder encounters an error.
type ErrCodecBuild struct {
	Message string
	Err     error
}

func (e ErrCodecBuild) Error() string {
	if e.Err == nil {
		return "cannot build " + e.Message
	}
	return "cannot build " + e.Message + ": " + e.Err.Error()
}

func newCodecBuildError(dataType string, a ...interface{}) *ErrCodecBuild {
	var err error
	var format, message string
	var ok bool
	if len(a) == 0 {
		return &ErrCodecBuild{dataType + ": no reason given", nil}
	}
	// if last item is error: save it
	if err, ok = a[len(a)-1].(error); ok {
		a = a[:len(a)-1] // pop it
	}
	// if items left, first ought to be format string
	if len(a) > 0 {
		if format, ok = a[0].(string); ok {
			a = a[1:] // unshift
			message = fmt.Sprintf(format, a...)
		}
	}
	if message != "" {
		message = ": " + message
	}
	return &ErrCodecBuild{dataType + message, err}
}

// Decoder interface specifies structures that may be decoded.
type Decoder interface {
	Decode(io.Reader) (interface{}, error)
}

// Encoder interface specifies structures that may be encoded.
type Encoder interface {
	Encode(io.Writer, interface{}) error
}

// JSONDecoder specifies structures that may be decoded as Avro JSON
type JSONDecoder interface {
	JSONDecode(io.Reader) (interface{}, error)
}

// JSONEncoder specifies structures that may be encoded as Avro JSON
type JSONEncoder interface {
	JSONEncode(io.Writer, interface{}) error
}

// The Codec interface supports both Decode and Encode operations.
type Codec interface {
	Decoder
	Encoder
	JSONDecoder
	JSONEncoder
	Schema() string
	NewWriter(...WriterSetter) (*Writer, error)
}

// CodecSetter functions are those those which are used to modify a
// newly instantiated Codec.
type CodecSetter func(Codec) error

type decoderFunction func(io.Reader) (interface{}, error)
type encoderFunction func(io.Writer, interface{}) error
type jsonDecoderFunction func(io.Reader) (interface{}, error)
type jsonEncoderFunction func(io.Writer, interface{}) error

type codec struct {
	nm     *name
	df     decoderFunction
	ef     encoderFunction
	jdf    jsonDecoderFunction
	jef    jsonEncoderFunction
	schema string
}

// String returns a string representation of the codec.
func (c codec) String() string {
	return fmt.Sprintf("nm: %v, df: %v, ef: %v", c.nm, c.df, c.ef)
}

// NOTE: use Go type names because for runtime resolution of
// union member, it gets the Go type name of the datum sent to
// the union encoder, and uses that string as a key into the
// encoders map
func newSymbolTable() *symtab {
	return &symtab{
		name:         make(map[string]*codec),
		nullCodec:    &codec{nm: &name{n: "null"}, df: nullDecoder, ef: nullEncoder, jdf: nullJSONDecoder, jef: nullJSONEncoder},
		booleanCodec: &codec{nm: &name{n: "bool"}, df: booleanDecoder, ef: booleanEncoder, jdf: booleanJSONDecoder, jef: booleanJSONEncoder},
		intCodec:     &codec{nm: &name{n: "int32"}, df: intDecoder, ef: intEncoder, jdf: intJSONDecoder, jef: intJSONEncoder},
		longCodec:    longCodec(),
		floatCodec:   &codec{nm: &name{n: "float32"}, df: floatDecoder, ef: floatEncoder, jdf: floatJSONDecoder, jef: floatJSONEncoder},
		doubleCodec:  &codec{nm: &name{n: "float64"}, df: doubleDecoder, ef: doubleEncoder, jdf: doubleJSONDecoder, jef: doubleJSONEncoder},
		bytesCodec:   &codec{nm: &name{n: "[]uint8"}, df: bytesDecoder, ef: bytesEncoder, jdf: bytesJSONDecoder, jef: bytesJSONEncoder},
		stringCodec:  &codec{nm: &name{n: "string"}, df: stringDecoder, ef: stringEncoder, jdf: stringJSONDecoder, jef: stringJSONEncoder},
	}
}

func longCodec() *codec {
	return &codec{nm: &name{n: "int64"}, df: longDecoder, ef: longEncoder, jdf: longJSONDecoder, jef: longJSONEncoder}
}

type symtab struct {
	name map[string]*codec // map full name to codec

	//cache primitive codecs
	nullCodec    *codec
	booleanCodec *codec
	intCodec     *codec
	longCodec    *codec
	floatCodec   *codec
	doubleCodec  *codec
	bytesCodec   *codec
	stringCodec  *codec
}

// NewCodec creates a new object that supports both the Decode and
// Encode methods. It requires an Avro schema, expressed as a JSON
// string.
//
//   codec, err := goavro.NewCodec(someJSONSchema)
//   if err != nil {
//       return nil, err
//   }
//
//   // Decoding data uses codec created above, and an io.Reader,
//   // definition not shown:
//   datum, err := codec.Decode(r)
//   if err != nil {
//       return nil, err
//   }
//
//   // Encoding data uses codec created above, an io.Writer,
//   // definition not shown, and some data:
//   err := codec.Encode(w, datum)
//   if err != nil {
//       return nil, err
//   }
//
//   // Encoding data using bufio.Writer to buffer the writes
//   // during data encoding:
//
//   func encodeWithBufferedWriter(c Codec, w io.Writer, datum interface{}) error {
//	bw := bufio.NewWriter(w)
//	err := c.Encode(bw, datum)
//	if err != nil {
//		return err
//	}
//	return bw.Flush()
//   }
//
//   err := encodeWithBufferedWriter(codec, w, datum)
//   if err != nil {
//       return nil, err
//   }
func NewCodec(someJSONSchema string, setters ...CodecSetter) (Codec, error) {
	// unmarshal into schema blob
	var schema interface{}
	if err := json.Unmarshal([]byte(someJSONSchema), &schema); err != nil {
		return nil, &ErrSchemaParse{"cannot unmarshal JSON", err}
	}
	// remarshal back into compressed json
	compressedSchema, err := json.Marshal(schema)
	if err != nil {
		return nil, fmt.Errorf("cannot marshal schema: %v", err)
	}

	// each codec gets a unified namespace of symbols to
	// respective codecs
	st := newSymbolTable()

	newCodec, err := st.buildCodec(nullNamespace, schema)
	if err != nil {
		return nil, err
	}

	for _, setter := range setters {
		err = setter(newCodec)
		if err != nil {
			return nil, err
		}
	}
	newCodec.schema = string(compressedSchema)
	return newCodec, nil
}

// Decode will read from the specified io.Reader, and return the next
// datum from the stream, or an error explaining why the stream cannot
// be converted into the Codec's schema.
func (c codec) Decode(r io.Reader) (interface{}, error) {
	return c.df(r)
}

// Encode will write the specified datum to the specified io.Writer,
// or return an error explaining why the datum cannot be converted
// into the Codec's schema.
func (c codec) Encode(w io.Writer, datum interface{}) error {
	return c.ef(w, datum)
}

// JSONDecode will read from the specified io.Reader, and return the next
// datum from the stream, or an error explaining why the stream cannot
// be converted into the Codec's schema.
func (c codec) JSONDecode(r io.Reader) (interface{}, error) {
	return c.jdf(r)
}

// JSONEncode will write the specified datum to the specified io.Writer,
// or return an error explaining why the datum cannot be converted
// into the Codec's schema.
func (c codec) JSONEncode(w io.Writer, datum interface{}) error {
	return c.jef(w, datum)
}

func (c codec) Schema() string {
	return c.schema
}

// NewWriter creates a new Writer that encodes using the given Codec.
//
// The following two code examples produce identical results:
//
//    // method 1:
//    fw, err := codec.NewWriter(goavro.ToWriter(w))
//    if err != nil {
//    	log.Fatal(err)
//    }
//    defer fw.Close()
//
//    // method 2:
//    fw, err := goavro.NewWriter(goavro.ToWriter(w), goavro.UseCodec(codec))
//    if err != nil {
//    	log.Fatal(err)
//    }
//    defer fw.Close()
func (c codec) NewWriter(setters ...WriterSetter) (*Writer, error) {
	setters = append(setters, UseCodec(c))
	return NewWriter(setters...)
}

func (st symtab) buildCodec(enclosingNamespace string, schema interface{}) (*codec, error) {
	switch schemaType := schema.(type) {
	case string:
		return st.buildString(enclosingNamespace, schemaType, schema)
	case []interface{}:
		return st.makeUnionCodec(enclosingNamespace, schema)
	case map[string]interface{}:
		return st.buildMap(enclosingNamespace, schema.(map[string]interface{}))
	default:
		return nil, newCodecBuildError("unknown", "schema type: %T", schema)
	}
}

func (st symtab) buildMap(enclosingNamespace string, schema map[string]interface{}) (*codec, error) {
	t, ok := schema["type"]
	if !ok {
		return nil, newCodecBuildError("map", "ought have type: %v", schema)
	}
	switch t.(type) {
	case string:
		// EXAMPLE: "type":"int"
		// EXAMPLE: "type":"enum"
		return st.buildString(enclosingNamespace, t.(string), schema)
	case map[string]interface{}, []interface{}:
		// EXAMPLE: "type":{"type":fixed","name":"fixed_16","size":16}
		// EXAMPLE: "type":["null","int"]
		return st.buildCodec(enclosingNamespace, t)
	default:
		return nil, newCodecBuildError("map", "type ought to be either string, map[string]interface{}, or []interface{}; received: %T", t)
	}
}

func (st symtab) buildString(enclosingNamespace, typeName string, schema interface{}) (*codec, error) {
	switch typeName {
	case "null":
		return st.nullCodec, nil
	case "boolean":
		return st.booleanCodec, nil
	case "int":
		return st.intCodec, nil
	case "long":
		return st.longCodec, nil
	case "float":
		return st.floatCodec, nil
	case "double":
		return st.doubleCodec, nil
	case "bytes":
		return st.bytesCodec, nil
	case "string":
		return st.stringCodec, nil
	case "record":
		return st.makeRecordCodec(enclosingNamespace, schema)
	case "enum":
		return st.makeEnumCodec(enclosingNamespace, schema)
	case "fixed":
		return st.makeFixedCodec(enclosingNamespace, schema)
	case "map":
		return st.makeMapCodec(enclosingNamespace, schema)
	case "array":
		return st.makeArrayCodec(enclosingNamespace, schema)
	default:
		t, err := newName(nameName(typeName), nameEnclosingNamespace(enclosingNamespace))
		if err != nil {
			return nil, newCodecBuildError(typeName, "could not normalize name: %q: %q: %s", enclosingNamespace, typeName, err)
		}
		c, ok := st.name[t.n]
		if !ok {
			return nil, newCodecBuildError("unknown", "unknown type name: %s", t.n)
		}
		return c, nil
	}
}

type unionEncoder struct {
	ef    encoderFunction
	jef   jsonEncoderFunction
	tn    string
	index int32
}

func getUnionTypeName(friendlyName string, enclosingNamespace string, schema interface{}) (string, error) {
	var typeName string
	switch schema.(type) {
	case string:
		typeName = schema.(string)
	case map[string]interface{}:
		schemaMap := schema.(map[string]interface{})
		t, ok := schemaMap["type"]
		if !ok {
			return "", newCodecBuildError(friendlyName, "type field is missing in :%v", schema)
		}
		switch t.(string) {
		case "record", "enum", "fixed":
			name, err := newName(nameSchema(schemaMap), nameEnclosingNamespace(enclosingNamespace))
			if err != nil {
				return "", err
			}
			return name.n, nil
		default:
			typeName = t.(string)
		}
	default:
		return "", newCodecBuildError(friendlyName, "unsupported type in union %v", schema)
	}
	switch typeName {
	default:
		name, err := newName(nameName(typeName), nameEnclosingNamespace(enclosingNamespace))
		if err != nil {
			return "", err
		}
		return name.n, nil
	case "null", "boolean", "int", "long", "float", "double", "bytes", "string":
		return typeName, nil
	}
}

func (st symtab) makeUnionCodec(enclosingNamespace string, schema interface{}) (*codec, error) {
	errorNamespace := "null namespace"
	if enclosingNamespace != nullNamespace {
		errorNamespace = enclosingNamespace
	}
	friendlyName := fmt.Sprintf("union (%s)", errorNamespace)

	// schema checks
	schemaArray, ok := schema.([]interface{})
	if !ok {
		return nil, newCodecBuildError(friendlyName, "ought to be array: %T", schema)
	}
	if len(schemaArray) == 0 {
		return nil, newCodecBuildError(friendlyName, " ought have at least one member")
	}

	// setup
	nameToUnionEncoder := make(map[string]unionEncoder)
	indexToDecoder := make([]decoderFunction, len(schemaArray))
	nameToJSONDecoder := make(map[string]jsonDecoderFunction)
	allowedNames := make([]string, len(schemaArray))

	for idx, unionMemberSchema := range schemaArray {
		c, err := st.buildCodec(enclosingNamespace, unionMemberSchema)
		if err != nil {
			return nil, newCodecBuildError(friendlyName, "member ought to be decodable: %s", err)
		}
		allowedNames[idx] = c.nm.n
		indexToDecoder[idx] = c.df
		unionType, err := getUnionTypeName(friendlyName, enclosingNamespace, unionMemberSchema)
		if err != nil {
			return nil, newCodecBuildError(friendlyName, "Can't get union type name: %s", err)
		}
		nameToJSONDecoder[unionType] = c.jdf
		nameToUnionEncoder[c.nm.n] = unionEncoder{ef: c.ef, jef: c.jef, tn: unionType, index: int32(idx)}
	}

	invalidType := "datum ought match schema: expected: "
	invalidType += strings.Join(allowedNames, ", ")
	invalidType += "; received: "

	nm, _ := newName(nameName("union"))
	friendlyName = fmt.Sprintf("union (%s)", nm.n)

	return &codec{
		nm: nm,
		df: func(r io.Reader) (interface{}, error) {
			i, err := intDecoder(r)
			if err != nil {
				return nil, newEncoderError(friendlyName, err)
			}
			idx, ok := i.(int32)
			if !ok {
				return nil, newEncoderError(friendlyName, "expected: int; received: %T", i)
			}
			index := int(idx)
			if index < 0 || index >= len(indexToDecoder) {
				return nil, newEncoderError(friendlyName, "index must be between 0 and %d; read index: %d", len(indexToDecoder)-1, index)
			}
			return indexToDecoder[index](r)
		},
		ef: func(w io.Writer, datum interface{}) error {
			var err error
			var name string
			switch datum.(type) {
			default:
				name = reflect.TypeOf(datum).String()
			case map[string]interface{}:
				name = "map"
			case []interface{}:
				name = "array"
			case nil:
				name = "null"
			case Enum:
				name = datum.(Enum).Name
			case Fixed:
				name = datum.(Fixed).Name
			case *Record:
				name = datum.(*Record).Name
			}

			ue, ok := nameToUnionEncoder[name]
			if !ok {
				return newEncoderError(friendlyName, invalidType+name)
			}
			if err = intEncoder(w, ue.index); err != nil {
				return newEncoderError(friendlyName, err)
			}
			if err = ue.ef(w, datum); err != nil {
				return newEncoderError(friendlyName, err)
			}
			return nil
		},
		jdf: func(r io.Reader) (interface{}, error) {
			// Convert from Avro JSON to regular JSON
			// 1. Parse using regular JSON decoder could be null or {"type": "value"}
			// 2. Figure out the JSON Avro decoder based on the type: null or "type"
			// 3. Marshal to bytes and then use the JSON Avro decoder: decode "value"

			// 1. Parse using regular JSON decoder
			var value interface{}
			decoder := json.NewDecoder(r)
			decoder.UseNumber()
			if err := decoder.Decode(&value); err != nil {
				return nil, newDecoderError(friendlyName, err)
			}

			// 2. Figure out the JSON Avro decoder based on the type
			// Lookup the type name
			var typeName string
			switch value.(type) {
			case nil:
				// Only allowed value for a non map in a union type
				typeName = "null"
			case map[string]interface{}:
				// Single key: value with key = type
				mapValue := value.(map[string]interface{})

				// extract the first and only key and value
				for k, v := range mapValue {
					typeName = k
					value = v
					break
				}
			default:
				return nil, newDecoderError(friendlyName, "unsupported union value %v", value)
			}

			// Type name extracted, lookup the JSON decoder func
			jsonDecoderFunc, ok := nameToJSONDecoder[typeName]
			if !ok {
				fmt.Printf("Union types are %v\n", nameToJSONDecoder)
				return nil, newDecoderError(friendlyName, "unknown union type %v", typeName)
			}

			// 3. Marshal to bytes and then use the JSON Avro decoder
			b, err := json.Marshal(value)
			if err != nil {
				return nil, newDecoderError(friendlyName, "union json decode failed: %v", err)
			}

			// Convert from Avro JSON to required data type
			return jsonDecoderFunc(bytes.NewReader(b))
		},
		jef: func(w io.Writer, datum interface{}) error {
			var err error

			// Convert to Avro JSON
			// 1. Lookup the union type
			// 2. JSON Encode the value
			// 3. Null is handled as is
			// 4. Embed the value in a JSON dict {type: value}

			// 1. Lookup the union type
			var typeName string
			switch datum.(type) {
			default:
				typeName = reflect.TypeOf(datum).String()
			case map[string]interface{}:
				typeName = "map"
			case []interface{}:
				typeName = "array"
			case nil:
				typeName = "null"
			case Enum:
				typeName = datum.(Enum).Name
			case Fixed:
				typeName = datum.(Fixed).Name
			case *Record:
				typeName = datum.(*Record).Name
			}

			// 2. JSON Encode the value
			ue, ok := nameToUnionEncoder[typeName]
			if !ok {
				return newEncoderError(friendlyName, "union json encode error: invalid type %v", typeName)
			}

			// 3. Null is handled as is
			if typeName == "null" {
				if err = ue.jef(w, datum); err != nil {
					return newEncoderError(friendlyName, "union json encode error: %v", err)
				}
				return nil
			}

			// 4. Embed the value in a JSON dict {type: value}
			// Convert into Avro JSON in a tmp writer
			var buff bytes.Buffer
			var value interface{}
			buffWriter := bufio.NewWriter(&buff)
			if err := ue.jef(buffWriter, datum); err != nil {
				return newEncoderError(friendlyName, "union json encode error: %v", err)
			}
			if err := buffWriter.Flush(); err != nil {
				return newEncoderError(friendlyName, "union json encode error: %v", err)
			}
			decoder := json.NewDecoder(bufio.NewReader(&buff))
			decoder.UseNumber()
			if err := decoder.Decode(&value); err != nil {
				return newEncoderError(friendlyName, err)
			}

			tmpDatum := map[string]interface{}{
				ue.tn: value,
			}
			b, err := json.Marshal(tmpDatum)
			if err != nil {
				return newEncoderError(friendlyName, "union json encode error: %v", err)
			}
			n, err := w.Write(b)
			if n < len(b) {
				return newEncoderError(friendlyName, "union json encode error: %v(%v)", n, len(b))
			}
			return nil
		},
	}, nil
}

// Enum is an abstract data type used to hold data corresponding to an Avro enum. Whenever an Avro
// schema specifies an enum, this library's Decode method will return an Enum initialized to the
// enum's name and value read from the io.Reader. Likewise, when using Encode to convert data to an
// Avro record, it is necessary to create and send an Enum instance to the Encode method.
type Enum struct {
	Name, Value string
}

func (st symtab) makeEnumCodec(enclosingNamespace string, schema interface{}) (*codec, error) {
	errorNamespace := "null namespace"
	if enclosingNamespace != nullNamespace {
		errorNamespace = enclosingNamespace
	}
	friendlyName := fmt.Sprintf("enum (%s)", errorNamespace)

	// schema checks
	schemaMap, ok := schema.(map[string]interface{})
	if !ok {
		return nil, newCodecBuildError(friendlyName, "expected: map[string]interface{}; received: %T", schema)
	}
	nm, err := newName(nameEnclosingNamespace(enclosingNamespace), nameSchema(schemaMap))
	if err != nil {
		return nil, err
	}
	friendlyName = fmt.Sprintf("enum (%s)", nm.n)

	s, ok := schemaMap["symbols"]
	if !ok {
		return nil, newCodecBuildError(friendlyName, "ought to have symbols key")
	}
	symtab, ok := s.([]interface{})
	if !ok || len(symtab) == 0 {
		return nil, newCodecBuildError(friendlyName, "symbols ought to be non-empty array")
	}
	for _, v := range symtab {
		_, ok := v.(string)
		if !ok {
			return nil, newCodecBuildError(friendlyName, "symbols array member ought to be string")
		}
	}
	c := &codec{
		nm: nm,
		df: func(r io.Reader) (interface{}, error) {
			someValue, err := longDecoder(r)
			if err != nil {
				return nil, newDecoderError(friendlyName, err)
			}
			index, ok := someValue.(int64)
			if !ok {
				return nil, newDecoderError(friendlyName, "expected long; received: %T", someValue)
			}
			if index < 0 || index >= int64(len(symtab)) {
				return nil, newDecoderError(friendlyName, "index must be between 0 and %d", len(symtab)-1)
			}
			return Enum{nm.n, symtab[index].(string)}, nil
		},
		ef: func(w io.Writer, datum interface{}) error {
			var someString string
			switch datum.(type) {
			case Enum:
				someString = datum.(Enum).Value
			case string:
				someString = datum.(string)
			default:
				return newEncoderError(friendlyName, "expected: Enum or string; received: %T", datum)
			}
			for idx, symbol := range symtab {
				if symbol == someString {
					if err := longEncoder(w, int64(idx)); err != nil {
						return newEncoderError(friendlyName, err)
					}
					return nil
				}
			}
			return newEncoderError(friendlyName, "symbol not defined: %s", someString)
		},
		jdf: func(r io.Reader) (interface{}, error) {
			// Enum is handled as a string
			someValue, err := newJSONDecoder("string")(r)
			if err != nil {
				return nil, newDecoderError(friendlyName, err)
			}
			return Enum{nm.n, someValue.(string)}, nil
		},
		jef: func(w io.Writer, datum interface{}) error {
			// Enum is handled as a string
			someEnum, ok := datum.(Enum)
			if !ok {
				return newEncoderError(friendlyName, "expected: Enum; received: %T", datum)
			}
			return newJSONEncoder("string")(w, someEnum.Value)
		},
	}
	st.name[nm.n] = c
	return c, nil
}

// Fixed is an abstract data type used to hold data corresponding to an Avro
// 'Fixed' type. Whenever an Avro schema specifies a "Fixed" type, this library's
// Decode method will return a Fixed value  initialized to the Fixed name, and
// value read from the io.Reader. Likewise, when using Encode to convert data to
// an Avro record, it is necessary to create and send a Fixed instance to the
// Encode method.
type Fixed struct {
	Name  string
	Value []byte
}

func (st symtab) makeFixedCodec(enclosingNamespace string, schema interface{}) (*codec, error) {
	errorNamespace := "null namespace"
	if enclosingNamespace != nullNamespace {
		errorNamespace = enclosingNamespace
	}
	friendlyName := fmt.Sprintf("fixed (%s)", errorNamespace)

	// schema checks
	schemaMap, ok := schema.(map[string]interface{})
	if !ok {
		return nil, newCodecBuildError(friendlyName, "expected: map[string]interface{}; received: %T", schema)
	}
	nm, err := newName(nameSchema(schemaMap), nameEnclosingNamespace(enclosingNamespace))
	if err != nil {
		return nil, err
	}
	friendlyName = fmt.Sprintf("fixed (%s)", nm.n)
	s, ok := schemaMap["size"]
	if !ok {
		return nil, newCodecBuildError(friendlyName, "ought to have size key")
	}
	fs, ok := s.(float64)
	if !ok {
		return nil, newCodecBuildError(friendlyName, "size ought to be number: %T", s)
	}
	size := int32(fs)
	c := &codec{
		nm: nm,
		df: func(r io.Reader) (interface{}, error) {
			buf := make([]byte, size)
			n, err := r.Read(buf)
			if err != nil {
				return nil, newDecoderError(friendlyName, err)
			}
			if n < int(size) {
				return nil, newDecoderError(friendlyName, "buffer underrun")
			}
			return Fixed{Name: nm.n, Value: buf}, nil
		},
		ef: func(w io.Writer, datum interface{}) error {
			someFixed, ok := datum.(Fixed)
			if !ok {
				return newEncoderError(friendlyName, "expected: Fixed; received: %T", datum)
			}
			if len(someFixed.Value) != int(size) {
				return newEncoderError(friendlyName, "expected: %d bytes; received: %d", size, len(someFixed.Value))
			}
			n, err := w.Write(someFixed.Value)
			if err != nil {
				return newEncoderError(friendlyName, err)
			}
			if n != int(size) {
				return newEncoderError(friendlyName, "buffer underrun")
			}
			return nil
		},
		jdf: func(r io.Reader) (interface{}, error) {
			someValue, err := newJSONDecoder("string")(r)
			if err != nil {
				return nil, newDecoderError(friendlyName, err)
			}
			someFixed := someValue.([]byte)
			if len(someFixed) < int(size) {
				return nil, newDecoderError(friendlyName, "buffer underrun")
			}
			return Fixed{nm.n, someFixed}, nil
		},
		jef: func(w io.Writer, datum interface{}) error {
			someFixed, ok := datum.(Fixed)
			if !ok {
				return newEncoderError(friendlyName, "expected: Fixed; received: %T", datum)
			}
			if len(someFixed.Value) != int(size) {
				return newEncoderError(friendlyName, "expected: %d bytes; received: %d", size, len(someFixed.Value))
			}
			return newJSONEncoder("string")(w, string(someFixed.Value))
		},
	}
	st.name[nm.n] = c
	return c, nil
}

type KeyVal struct {
	Key string
	Val interface{}
}

// Define an ordered map
type OrderedMap []KeyVal

// Implement the json.Marshaler interface
func (omap OrderedMap) MarshalJSON() ([]byte, error) {
	var buf bytes.Buffer

	buf.WriteString("{")
	for i, kv := range omap {
		if i != 0 {
			buf.WriteString(",")
		}
		// marshal key
		key, err := json.Marshal(kv.Key)
		if err != nil {
			return nil, err
		}
		buf.Write(key)
		buf.WriteString(":")
		// marshal value
		val, err := json.Marshal(kv.Val)
		if err != nil {
			return nil, err
		}
		buf.Write(val)
	}

	buf.WriteString("}")
	return buf.Bytes(), nil
}

func (st symtab) makeRecordCodec(enclosingNamespace string, schema interface{}) (*codec, error) {
	errorNamespace := "null namespace"
	if enclosingNamespace != nullNamespace {
		errorNamespace = enclosingNamespace
	}
	friendlyName := fmt.Sprintf("record (%s)", errorNamespace)

	// delegate schema checks to NewRecord()
	recordTemplate, err := NewRecord(recordSchemaRaw(schema), RecordEnclosingNamespace(enclosingNamespace))
	if err != nil {
		return nil, err
	}

	if len(recordTemplate.Fields) == 0 {
		return nil, newCodecBuildError(friendlyName, "fields ought to be non-empty array")
	}

	fieldCodecs := make([]*codec, len(recordTemplate.Fields))
	fieldCodecMap := make(map[string]*codec)
	for idx, field := range recordTemplate.Fields {
		var err error
		fieldCodecs[idx], err = st.buildCodec(recordTemplate.n.namespace(), field.schema)
		if err != nil {
			return nil, newCodecBuildError(friendlyName, "record field ought to be codec: %+v", st, err)
		}
		fieldCodecMap[field.Name] = fieldCodecs[idx]
	}

	friendlyName = fmt.Sprintf("record (%s)", recordTemplate.Name)

	c := &codec{
		nm: recordTemplate.n,
		df: func(r io.Reader) (interface{}, error) {
			someRecord, _ := NewRecord(recordSchemaRaw(schema), RecordEnclosingNamespace(enclosingNamespace))
			for idx, codec := range fieldCodecs {
				value, err := codec.Decode(r)
				if err != nil {
					return nil, newDecoderError(friendlyName, err)
				}
				someRecord.Fields[idx].Datum = value
			}
			return someRecord, nil
		},
		ef: func(w io.Writer, datum interface{}) error {
			someRecord, ok := datum.(*Record)
			if !ok {
				return newEncoderError(friendlyName, "expected: Record; received: %T", datum)
			}
			if someRecord.Name != recordTemplate.Name {
				return newEncoderError(friendlyName, "expected: %v; received: %v", recordTemplate.Name, someRecord.Name)
			}
			for idx, field := range someRecord.Fields {
				var value interface{}
				// check whether field datum is valid
				if reflect.ValueOf(field.Datum).IsValid() {
					value = field.Datum
				} else if field.hasDefault {
					value = field.defval
				} else {
					return newEncoderError(friendlyName, "field has no data and no default set: %v", field.Name)
				}
				err = fieldCodecs[idx].Encode(w, value)
				if err != nil {
					return newEncoderError(friendlyName, err)
				}
			}
			return nil
		},
		jdf: func(r io.Reader) (interface{}, error) {
			someRecord, _ := NewRecord(recordSchemaRaw(schema), RecordEnclosingNamespace(enclosingNamespace))

			// Read into regular JSON map
			var datum map[string]interface{}
			decoder := json.NewDecoder(r)
			decoder.UseNumber()
			if err := decoder.Decode(&datum); err != nil {
				return nil, newDecoderError(friendlyName, err)
			}

			// Convert each field from Avro to regular structure
			for key, value := range datum {
				b, err := json.Marshal(value)
				if err != nil {
					return nil, newDecoderError(friendlyName, err)
				}
				field, err := someRecord.getField(key)
				if err != nil {
					return nil, newDecoderError(friendlyName, err)
				}
				fieldDatum, err := fieldCodecMap[field.Name].JSONDecode(bytes.NewBuffer(b))
				if err != nil {
					return nil, newDecoderError(friendlyName, err)
				}
				field.Datum = fieldDatum
			}
			return someRecord, nil
		},
		jef: func(w io.Writer, datum interface{}) error {
			someRecord, ok := datum.(*Record)
			if !ok {
				return newEncoderError(friendlyName, "expected: Record; received: %T", datum)
			}
			if someRecord.Name != recordTemplate.Name {
				return newEncoderError(friendlyName, "expected: %v; received: %v", recordTemplate.Name, someRecord.Name)
			}

			var orderedMap OrderedMap
			//jsonMap := make(map[string]interface{})
			for idx, field := range someRecord.Fields {
				var value interface{}
				// check whether field datum is valid
				if reflect.ValueOf(field.Datum).IsValid() {
					value = field.Datum
				} else if field.hasDefault {
					value = field.defval
				} else {
					return newEncoderError(friendlyName, "field has no data and no default set: %v", field.Name)
				}
				var buff bytes.Buffer
				tmpWriter := bufio.NewWriter(&buff)
				err = fieldCodecs[idx].JSONEncode(tmpWriter, value)
				if err != nil {
					return newEncoderError(friendlyName, err)
				}
				if err := tmpWriter.Flush(); err != nil {
					return newEncoderError(friendlyName, "record json encode error: %v", err)
				}
				var jsonValue interface{}
				decoder := json.NewDecoder(bufio.NewReader(&buff))
				decoder.UseNumber()
				err := decoder.Decode(&jsonValue)
				if err != nil {
					return newEncoderError(friendlyName, err)
				}
				n, err := newName(nameName(field.Name))
				if err != nil {
					return newEncoderError(friendlyName, err)
				}
				orderedMap = append(orderedMap, KeyVal{n.shortname(), jsonValue})
				//jsonMap[n.shortname()] = jsonValue
			}
			//b, err := json.Marshal(jsonMap)
			b, err := json.Marshal(orderedMap)
			if err != nil {
				return newEncoderError(friendlyName, "record json encode error: %v", err)
			}
			n, err := w.Write(b)
			if n < len(b) {
				return newEncoderError(friendlyName, "record json encode error: %v(%v)", n, len(b))
			}
			return nil
		},
	}
	st.name[recordTemplate.Name] = c
	return c, nil
}

func (st symtab) makeMapCodec(enclosingNamespace string, schema interface{}) (*codec, error) {
	errorNamespace := "null namespace"
	if enclosingNamespace != nullNamespace {
		errorNamespace = enclosingNamespace
	}
	friendlyName := fmt.Sprintf("map (%s)", errorNamespace)

	// schema checks
	schemaMap, ok := schema.(map[string]interface{})
	if !ok {
		return nil, newCodecBuildError(friendlyName, "expected: map[string]interface{}; received: %T", schema)
	}
	v, ok := schemaMap["values"]
	if !ok {
		return nil, newCodecBuildError(friendlyName, "ought to have values key")
	}
	valuesCodec, err := st.buildCodec(enclosingNamespace, v)
	if err != nil {
		return nil, newCodecBuildError(friendlyName, err)
	}

	nm := &name{n: "map"}
	friendlyName = fmt.Sprintf("map (%s)", nm.n)

	return &codec{
		nm: nm,
		df: func(r io.Reader) (interface{}, error) {
			data := make(map[string]interface{})
			someValue, err := longDecoder(r)
			if err != nil {
				return nil, newDecoderError(friendlyName, err)
			}
			blockCount := someValue.(int64)

			for blockCount != 0 {
				if blockCount < 0 {
					blockCount = -blockCount
					// next long is size of block, for which we have no use
					_, err := longDecoder(r)
					if err != nil {
						return nil, newDecoderError(friendlyName, err)
					}
				}
				for i := int64(0); i < blockCount; i++ {
					someValue, err := stringDecoder(r)
					if err != nil {
						return nil, newDecoderError(friendlyName, err)
					}
					mapKey, ok := someValue.(string)
					if !ok {
						return nil, newDecoderError(friendlyName, "map key ought to be string")
					}
					datum, err := valuesCodec.df(r)
					if err != nil {
						return nil, err
					}
					data[mapKey] = datum
				}
				// decode next blockcount
				someValue, err = longDecoder(r)
				if err != nil {
					return nil, newDecoderError(friendlyName, err)
				}
				blockCount = someValue.(int64)
			}
			return data, nil
		},
		ef: func(w io.Writer, datum interface{}) error {
			dict, ok := datum.(map[string]interface{})
			if !ok {
				return newEncoderError(friendlyName, "expected: map[string]interface{}; received: %T", datum)
			}
			if len(dict) > 0 {
				if err = longEncoder(w, int64(len(dict))); err != nil {
					return newEncoderError(friendlyName, err)
				}
				for k, v := range dict {
					if err = stringEncoder(w, k); err != nil {
						return newEncoderError(friendlyName, err)
					}
					if err = valuesCodec.ef(w, v); err != nil {
						return newEncoderError(friendlyName, err)
					}
				}
			}
			if err = longEncoder(w, int64(0)); err != nil {
				return newEncoderError(friendlyName, err)
			}
			return nil
		},
		jdf: func(r io.Reader) (interface{}, error) {
			var err error
			var datum interface{}
			var b []byte
			var rawDatum map[string]interface{}
			data := make(map[string]interface{})

			decoder := json.NewDecoder(r)
			decoder.UseNumber()
			if err = decoder.Decode(&rawDatum); err != nil {
				return nil, newDecoderError(friendlyName, err)
			}
			for k, v := range rawDatum {
				if b, err = json.Marshal(v); err != nil {
					return nil, newDecoderError(friendlyName, err)
				}
				if datum, err = valuesCodec.JSONDecode(bytes.NewReader(b)); err != nil {
					return nil, newDecoderError(friendlyName, err)
				}
				data[k] = datum
			}
			return data, nil
		},
		jef: func(w io.Writer, datum interface{}) error {
			dict, ok := datum.(map[string]interface{})
			if !ok {
				return newEncoderError(friendlyName, "expected: map[string]interface{}; received: %T", datum)
			}
			jsonDict := make(map[string]interface{})
			for k, v := range dict {
				var buff bytes.Buffer
				var jsonObj interface{}
				writer := bufio.NewWriter(&buff)
				if err := valuesCodec.JSONEncode(writer, v); err != nil {
					return newEncoderError(friendlyName, err)
				}
				err := writer.Flush()
				if err != nil {
					return newEncoderError(friendlyName, err)
				}
				decoder := json.NewDecoder(bufio.NewReader(&buff))
				decoder.UseNumber()
				if err := decoder.Decode(&jsonObj); err != nil {
					return newEncoderError(friendlyName, err)
				}
				jsonDict[k] = jsonObj
			}
			b, err := json.Marshal(jsonDict)
			if err != nil {
				return newEncoderError(friendlyName, err)
			}
			n, err := w.Write(b)
			if err != nil {
				return newEncoderError(friendlyName, err)
			}
			if n < len(b) {
				return newEncoderError(friendlyName, "map encode error %v(%v)", n, len(b))
			}
			return nil
		},
	}, nil
}

func (st symtab) makeArrayCodec(enclosingNamespace string, schema interface{}) (*codec, error) {
	errorNamespace := "null namespace"
	if enclosingNamespace != nullNamespace {
		errorNamespace = enclosingNamespace
	}
	friendlyName := fmt.Sprintf("array (%s)", errorNamespace)

	// schema checks
	schemaMap, ok := schema.(map[string]interface{})
	if !ok {
		return nil, newCodecBuildError(friendlyName, "expected: map[string]interface{}; received: %T", schema)
	}
	v, ok := schemaMap["items"]
	if !ok {
		return nil, newCodecBuildError(friendlyName, "ought to have items key")
	}
	valuesCodec, err := st.buildCodec(enclosingNamespace, v)
	if err != nil {
		return nil, newCodecBuildError(friendlyName, err)
	}

	const itemsPerArrayBlock = 10
	nm := &name{n: "array"}
	friendlyName = fmt.Sprintf("array (%s)", nm.n)

	return &codec{
		nm: nm,
		df: func(r io.Reader) (interface{}, error) {
			var data []interface{}

			someValue, err := longDecoder(r)
			if err != nil {
				return nil, newDecoderError(friendlyName, err)
			}
			blockCount := someValue.(int64)

			for blockCount != 0 {
				if blockCount < 0 {
					blockCount = -blockCount
					// read and discard number of bytes in block
					_, err = longDecoder(r)
					if err != nil {
						return nil, newDecoderError(friendlyName, err)
					}
				}
				for i := int64(0); i < blockCount; i++ {
					datum, err := valuesCodec.df(r)
					if err != nil {
						return nil, newDecoderError(friendlyName, err)
					}
					data = append(data, datum)
				}
				someValue, err = longDecoder(r)
				if err != nil {
					return nil, newDecoderError(friendlyName, err)
				}
				blockCount = someValue.(int64)
			}
			return data, nil
		},
		ef: func(w io.Writer, datum interface{}) error {
			someArray, ok := datum.([]interface{})
			if !ok {
				return newEncoderError(friendlyName, "expected: []interface{}; received: %T", datum)
			}
			for leftIndex := 0; leftIndex < len(someArray); leftIndex += itemsPerArrayBlock {
				rightIndex := leftIndex + itemsPerArrayBlock
				if rightIndex > len(someArray) {
					rightIndex = len(someArray)
				}
				items := someArray[leftIndex:rightIndex]
				err = longEncoder(w, int64(len(items)))
				if err != nil {
					return newEncoderError(friendlyName, err)
				}
				for _, item := range items {
					err = valuesCodec.ef(w, item)
					if err != nil {
						return newEncoderError(friendlyName, err)
					}
				}
			}
			return longEncoder(w, int64(0))
		},
		jdf: func(r io.Reader) (interface{}, error) {
			var err error
			var rawJsonArray []interface{}
			decoder := json.NewDecoder(r)
			decoder.UseNumber()
			if err = decoder.Decode(&rawJsonArray); err != nil {
				return nil, newDecoderError(friendlyName, err)
			}

			var jsonArray []interface{}
			for _, jsonValue := range rawJsonArray {
				var datum interface{}
				var b []byte
				if b, err = json.Marshal(jsonValue); err != nil {
					return nil, newDecoderError(friendlyName, err)
				}
				if datum, err = valuesCodec.JSONDecode(bytes.NewReader(b)); err != nil {
					return nil, newDecoderError(friendlyName, err)
				}
				jsonArray = append(jsonArray, datum)
			}
			return jsonArray, nil
		},
		jef: func(w io.Writer, datum interface{}) error {
			// Check if it is an array
			someArray, ok := datum.([]interface{})
			if !ok {
				return newEncoderError(friendlyName, "expected: []interface{}; received: %T", datum)
			}
			// Convert each value and then back to Avro JSON before doing a final encode.
			var avroArray []interface{}
			for _, someValue := range someArray {
				var buff bytes.Buffer
				var jsonObj interface{}
				writer := bufio.NewWriter(&buff)
				if err := valuesCodec.JSONEncode(writer, someValue); err != nil {
					return newEncoderError(friendlyName, err)
				}
				if err := writer.Flush(); err != nil {
					return newEncoderError(friendlyName, "array json encode error: %v", err)
				}
				decoder := json.NewDecoder(bufio.NewReader(&buff))
				decoder.UseNumber()
				if err := decoder.Decode(&jsonObj); err != nil {
					return newEncoderError(friendlyName, err)
				}
				avroArray = append(avroArray, jsonObj)
			}
			b, err := json.Marshal(avroArray)
			if err != nil {
				return newEncoderError(friendlyName, "array json encode error: %v", err)
			}
			n, err := w.Write(b)
			if n < len(b) {
				return newEncoderError(friendlyName, "array json encode error: %v(%v)", n, len(b))
			}
			return nil
		},
	}, nil
}
