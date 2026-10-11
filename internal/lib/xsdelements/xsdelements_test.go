package xsdelements

import (
	"bytes"
	"reflect"
	"strings"
	"testing"
)

func TestRead(t *testing.T) {
	const xs = `xmlns:xs="http://www.w3.org/2001/XMLSchema"`
	tests := []struct {
		name   string
		schema string
		want   Elements
	}{
		{
			"global, local and many-typed",
			`<xs:schema ` + xs + ` targetNamespace="urn:x" elementFormDefault="qualified">
				<xs:element name="item" type="Item"/>
				<xs:element name="body" type="Body"/>
				<xs:complexType name="Item"><xs:sequence>
					<xs:element name="body" type="Body"/>
					<xs:element name="note" type="ItemNote"/>
					<xs:element name="same" type="Same"/>
					<xs:element ref="item"/>
					<xs:element name="plain" form="unqualified"/>
				</xs:sequence></xs:complexType>
				<xs:group name="g"><xs:choice>
					<xs:element name="note" type="GroupNote"/>
					<xs:element name="same" type="Same"/>
					<xs:element name="inline"><xs:complexType/></xs:element>
					<xs:element name="inline"><xs:complexType/></xs:element>
				</xs:choice></xs:group>
			</xs:schema>`,
			Elements{
				Namespace: "urn:x",
				Global:    []string{"body", "item"},
				Local:     []string{"body", "inline", "note", "same"},
				// "body" has one type in both places, and "same" too; two
				// declarations without a type attribute are two types.
				ManyTyped: []string{"inline", "note"},
			},
		},
		{
			"local elements are unqualified by default",
			`<xs:schema ` + xs + ` targetNamespace="urn:x">
				<xs:element name="root"><xs:complexType><xs:sequence>
					<xs:element name="unqualified"/>
					<xs:element name="qualified" form="qualified"/>
				</xs:sequence></xs:complexType></xs:element>
			</xs:schema>`,
			Elements{Namespace: "urn:x", Global: []string{"root"}, Local: []string{"qualified"}, ManyTyped: []string{}},
		},
		{
			"no elements",
			`<xs:schema ` + xs + `/>`,
			Elements{Global: []string{}, Local: []string{}, ManyTyped: []string{}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Read(strings.NewReader(tt.schema))
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("got  %+v\nwant %+v", got, tt.want)
			}
		})
	}
}

func TestReadRejectsWhatIsNoSchema(t *testing.T) {
	for name, tc := range map[string]struct{ doc, want string }{
		"another root":  {`<schema xmlns="urn:other"/>`, "root element is not xs:schema"},
		"empty":         {``, "no root element"},
		"not XML":       {`<xs:schema`, "XML syntax error"},
		"not closed":    {`<xs:schema xmlns:xs="http://www.w3.org/2001/XMLSchema">`, "XML syntax error"},
		"only a prolog": {`<?xml version="1.0"?>`, "no root element"},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := Read(strings.NewReader(tc.doc)); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("got %v, want an error with %q", err, tc.want)
			}
		})
	}
}

func TestCompiledRoundTrip(t *testing.T) {
	c := &Compiled{Version: CompiledVersion, Schemas: map[string]Schema{
		"https://example.org/a.xsd": {SHA256: "ab", Elements: Elements{
			Namespace: "urn:a", Global: []string{"a"}, Local: []string{"b", "c"}, ManyTyped: []string{},
		}},
	}}
	var buf bytes.Buffer
	if err := c.Encode(&buf); err != nil {
		t.Fatal(err)
	}
	got, err := Decode(bytes.NewReader(buf.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, c) {
		t.Errorf("got %+v, want %+v", got, c)
	}

	// Another format version is refused: its lists may mean something else.
	c.Version++
	buf.Reset()
	if err := c.Encode(&buf); err != nil {
		t.Fatal(err)
	}
	if _, err := Decode(bytes.NewReader(buf.Bytes())); err == nil || !strings.Contains(err.Error(), "format version") {
		t.Errorf("got %v, want the format version refused", err)
	}
	if _, err := Decode(strings.NewReader("not gzip")); err == nil {
		t.Error("decoded what is not gzip")
	}
}
