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

package goavro

import (
	"io"
	"encoding/json"
)

func newJSONDecoder(goType string) jsonDecoderFunction {
	return func(r io.Reader) (interface{}, error) {
		dec := json.NewDecoder(r)
		dec.UseNumber()
		var datum interface{}
		if err := dec.Decode(&datum); err != nil {
			return nil, newDecoderError(goType, err)
		}
		return datum, nil
	}
}

func nullJSONDecoder(r io.Reader) (interface{}, error) {
	return newJSONDecoder("nil")(r)
}

func booleanJSONDecoder(r io.Reader) (interface{}, error) {
	return newJSONDecoder("boolean")(r)
}

func intJSONDecoder(r io.Reader) (interface{}, error) {
	someValue, err := newJSONDecoder("int")(r)
 	if err != nil {
		return nil, err
	}
	someNumber, ok := someValue.(json.Number)
	if !ok {
		return nil, newDecoderError("int", "expected json.Number: received %T", someNumber)
	}
	someInt, err := someNumber.Int64()
	if err != nil {
		return nil, newDecoderError("int", "expected int64: received %v", someNumber)
	}
        return int32(someInt), nil
}

func longJSONDecoder(r io.Reader) (interface{}, error) {
	someValue, err := newJSONDecoder("long")(r)
 	if err != nil {
		return nil, err
	}
	someNumber, ok := someValue.(json.Number)
	if !ok {
		return nil, newDecoderError("long", "expected json.Number: received %T", someNumber)
	}
	return someNumber.Int64()
}

func floatJSONDecoder(r io.Reader) (interface{}, error) {
	someValue, err := newJSONDecoder("float")(r)
 	if err != nil {
		return nil, err
	}
	someNumber, ok := someValue.(json.Number)
	if !ok {
		return nil, newDecoderError("float", "expected json.Number: received %T", someNumber)
	}
	someFloat, err := someNumber.Float64()
	if err != nil {
		return nil, newDecoderError("int", "expected : float64 received %v", someNumber)
	}
        return float32(someFloat), nil
}

func doubleJSONDecoder(r io.Reader) (interface{}, error) {
	someValue, err := newJSONDecoder("double")(r)
 	if err != nil {
		return nil, err
	}
	someNumber, ok := someValue.(json.Number)
	if !ok {
		return nil, newDecoderError("double", "expected json.Number: received %T", someNumber)
	}
        return someNumber.Float64()
}

func bytesJSONDecoder(r io.Reader) (interface{}, error) {
	someValue, err := newJSONDecoder("bytes")(r)
	if err != nil {
		return nil, err
	}
	someString, ok := someValue.(string)
	if !ok {
		return nil, newDecoderError("bytes", "expected string: received %T", someValue)
	}
	return []byte(someString), nil
}

func stringJSONDecoder(r io.Reader) (interface{}, error) {
        return newJSONDecoder("string")(r)
}
