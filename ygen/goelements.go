// Copyright 2017 Google Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package ygen

import (
	"bytes"
	"fmt"

	"github.com/openconfig/goyang/pkg/yang"
	"github.com/openconfig/ygot/genutil"
	"github.com/openconfig/ygot/util"
	"github.com/openconfig/ygot/ygot"
)

const (
	// goEnumPrefix is the prefix that is used for type names in the output
	// Go code, such that an enumeration's name is of the form
	//   <goEnumPrefix><EnumName>
	goEnumPrefix string = "E_"
)

var (
	// validGoBuiltinTypes stores the valid types that the Go code generation
	// produces, such that resolved types can be checked as to whether they are
	// Go built in types.
	validGoBuiltinTypes = map[string]bool{
		"int8":              true,
		"int16":             true,
		"int32":             true,
		"int64":             true,
		"uint8":             true,
		"uint16":            true,
		"uint32":            true,
		"uint64":            true,
		"float64":           true,
		"string":            true,
		"bool":              true,
		"interface{}":       true,
		ygot.BinaryTypeName: true,
		ygot.EmptyTypeName:  true,
	}

	// goZeroValues stores the defined zero value for the Go types that can
	// be used within a generated struct. It is used when leaf getters are
	// generated to return a zero value rather than the set value.
	goZeroValues = map[string]string{
		"int8":              "0",
		"int16":             "0",
		"int32":             "0",
		"int64":             "0",
		"uint8":             "0",
		"uint16":            "0",
		"uint32":            "0",
		"uint64":            "0",
		"float64":           "0.0",
		"string":            `""`,
		"bool":              "false",
		"interface{}":       "nil",
		ygot.BinaryTypeName: "nil",
		ygot.EmptyTypeName:  "false",
	}
)

// goGenState contains the functionality and state for generating Go names for
// the generated code.
type goGenState struct {
	// enumSet contains the generated enum names which can be queried.
	enumSet *enumSet
	// schematree is a copy of the YANG schema tree, containing only leaf
	// entries, such that schema paths can be referenced.
	schematree *schemaTree
	// definedGlobals specifies the global Go names used during code
	// generation to avoid conflicts.
	definedGlobals map[string]bool
	// uniqueDirectoryNames is a map keyed by the path of a YANG entity representing a
	// directory in the generated code whose value is the unique name that it
	// was mapped to. This allows routines to determine, based on a particular YANG
	// entry, how to refer to it when generating code.
	uniqueDirectoryNames map[string]string
}

// newGoGenState creates a new goGenState instance, initialised with the
// default state required for code generation.
func newGoGenState(schematree *schemaTree, eSet *enumSet) *goGenState {
	return &goGenState{
		enumSet:    eSet,
		schematree: schematree,
		definedGlobals: map[string]bool{
			// Mark the name that is used for the binary type as a reserved name
			// within the output structs.
			ygot.BinaryTypeName: true,
			ygot.EmptyTypeName:  true,
		},
		uniqueDirectoryNames: map[string]string{},
	}
}

// resolveTypeArgs is a structure used as an input argument to the yangTypeToGoType
// function which allows extra context to be handed on. This provides the ability
// to use not only the YangType but also the yang.Entry that the type was part of
// to resolve the possible type name.
type resolveTypeArgs struct {
	// yangType is a pointer to the yang.YangType that is to be mapped.
	yangType *yang.YangType
	// contextEntry is an optional yang.Entry which is supplied where a
	// type requires knowledge of the leaf that it is used within to be
	// mapped. For example, where a leaf is defined to have a type of a
	// user-defined type (typedef) that in turn has enumerated values - the
	// context of the yang.Entry is required such that the leaf's context
	// can be established.
	contextEntry *yang.Entry
}

// TODO(robjs): When adding support for other language outputs, we should restructure
// the code such that we do not have genState receivers here, but rather pass in the
// generated state as a parameter to the function that is being called.

// pathToCamelCaseName takes an input yang.Entry and outputs its name as a Go
// compatible name in the form PathElement1_PathElement2, performing schema
// compression if required. The name is not checked for uniqueness. The
// genFakeRoot boolean specifies whether the fake root exists within the schema
// such that it can be handled specifically in the path generation.
// TODO(wenbli): Move this to genutil.
func pathToCamelCaseName(e *yang.Entry, compressOCPaths, genFakeRoot bool) string {
	var pathElements []*yang.Entry

	if genFakeRoot && IsFakeRoot(e) {
		// Handle the special case of the root element if it exists.
		pathElements = []*yang.Entry{e}
	} else {
		// Determine the set of elements that make up the path back to the root of
		// the element supplied.
		element := e
		for element != nil {
			// If the CompressOCPaths option is set to true, then only append the
			// element to the path if the element itself would have code generated
			// for it - this compresses out surrounding containers, config/state
			// containers and root modules.
			if compressOCPaths && util.IsOCCompressedValidElement(element) || !compressOCPaths && !util.IsChoiceOrCase(element) {
				pathElements = append(pathElements, element)
			}
			element = element.Parent
		}
	}

	// Iterate through the pathElements slice backwards to build up the name
	// of the form CamelCaseElementOne_CamelCaseElementTwo.
	var buf bytes.Buffer
	for i := range pathElements {
		idx := len(pathElements) - 1 - i
		buf.WriteString(genutil.EntryCamelCaseName(pathElements[idx]))
		if idx != 0 {
			buf.WriteRune('_')
		}
	}

	return buf.String()
}

// goStructName generates the name to be used for a particular YANG schema
// element in the generated Go code. If the compressOCPaths boolean is set to
// true, schemapaths are compressed, otherwise the name is returned simply as
// camel case. The genFakeRoot boolean specifies whether the fake root is to be
// generated such that the struct name can consider the fake root entity
// specifically.
func (s *goGenState) goStructName(e *yang.Entry, compressOCPaths, genFakeRoot bool) string {
	uniqName := genutil.MakeNameUnique(pathToCamelCaseName(e, compressOCPaths, genFakeRoot), s.definedGlobals)

	// Record the name of the struct that was unique such that it can be referenced
	// by path.
	s.uniqueDirectoryNames[e.Path()] = uniqName

	return uniqName
}

// buildDirectoryDefinitions extracts the yang.Entry instances from a map of
// entries that need struct definitions built for them. It resolves each
// non-leaf yang.Entry to a Directory which contains the elements that are
// needed for subsequent code generation, with the relationships between the
// elements being determined by the compress behaviour and genFakeRoot (whether
// a fake root element is generated). The skipEnumDedup argument specifies to
// the code generation whether to try to output a single type for an
// enumeration that is logically defined once in the output code, but
// instantiated in multiple places in the schema tree.  The skipEnumDedup
// argument specifies whether leaves of type 'enumeration' which are used more
// than once in the schema should use a common output type in the generated Go
// code. By default a type is shared.
func (s *goGenState) buildDirectoryDefinitions(entries map[string]*yang.Entry, compBehaviour genutil.CompressBehaviour, genFakeRoot, skipEnumDedup, shortenEnumLeafNames bool) (map[string]*Directory, []error) {
	return buildDirectoryDefinitions(entries, compBehaviour,
		// For Go, we map the name of the struct to the path elements
		// in CamelCase separated by underscores.
		func(e *yang.Entry) string {
			return s.goStructName(e, compBehaviour.CompressEnabled(), genFakeRoot)
		},
		func(keyleaf *yang.Entry) (*MappedType, error) {
			return s.yangTypeToGoType(resolveTypeArgs{yangType: keyleaf.Type, contextEntry: keyleaf}, compBehaviour.CompressEnabled(), skipEnumDedup, shortenEnumLeafNames)
		})
}

// yangTypeToGoType takes a yang.YangType (YANG type definition) and maps it
// to the type that should be used to represent it in the generated Go code.
// A resolveTypeArgs structure is used as the input argument which specifies a
// pointer to the YangType; and optionally context required to resolve the name
// of the type. The compressOCPaths argument specifies whether compression of
// OpenConfig paths is to be enabled. The skipEnumDedup argument specifies whether
// the current schema is set to deduplicate enumerations that are logically defined
// once in the YANG schema, but instantiated in multiple places.
// The skipEnumDedup argument specifies whether leaves of type enumeration that are
// used more than once in the schema should share a common type. By default, a single
// type for each leaf is created.
func (s *goGenState) yangTypeToGoType(args resolveTypeArgs, compressOCPaths, skipEnumDedup, shortenEnumLeafNames bool) (*MappedType, error) {
	defVal := genutil.TypeDefaultValue(args.yangType)
	// Handle the case of a typedef which is actually an enumeration.

	// TODO(robjs): change this to be a call to Lookup
	mtype, err := s.enumSet.LookupTypedef(args.yangType, args.contextEntry)
	//mtype, err := s.enumSet.enumeratedTypedefTypeName(args)
	if err != nil {
		// err is non nil when this was a typedef which included
		// an invalid enumerated type.
		return nil, err
	}

	if mtype != nil {
		// mtype is set to non-nil when this was a valid enumeration
		// within a typedef. We explicitly set the zero and default values
		// here.
		mtype.ZeroValue = "0"
		if defVal != nil {
			mtype.DefaultValue = enumDefaultValue(mtype.NativeType, *defVal, goEnumPrefix)
		}

		return mtype, nil
	}

	// Perform the actual mapping of the type to the Go type.
	switch args.yangType.Kind {
	case yang.Yint8:
		return &MappedType{NativeType: "int8", ZeroValue: goZeroValues["int8"], DefaultValue: defVal}, nil
	case yang.Yint16:
		return &MappedType{NativeType: "int16", ZeroValue: goZeroValues["int16"], DefaultValue: defVal}, nil
	case yang.Yint32:
		return &MappedType{NativeType: "int32", ZeroValue: goZeroValues["int32"], DefaultValue: defVal}, nil
	case yang.Yint64:
		return &MappedType{NativeType: "int64", ZeroValue: goZeroValues["int64"], DefaultValue: defVal}, nil
	case yang.Yuint8:
		return &MappedType{NativeType: "uint8", ZeroValue: goZeroValues["uint8"], DefaultValue: defVal}, nil
	case yang.Yuint16:
		return &MappedType{NativeType: "uint16", ZeroValue: goZeroValues["uint16"], DefaultValue: defVal}, nil
	case yang.Yuint32:
		return &MappedType{NativeType: "uint32", ZeroValue: goZeroValues["uint32"], DefaultValue: defVal}, nil
	case yang.Yuint64:
		return &MappedType{NativeType: "uint64", ZeroValue: goZeroValues["uint64"], DefaultValue: defVal}, nil
	case yang.Ybool:
		return &MappedType{NativeType: "bool", ZeroValue: goZeroValues["bool"], DefaultValue: defVal}, nil
	case yang.Yempty:
		// Empty is a YANG type that either exists or doesn't, therefore
		// map it to a boolean to indicate its presence or not. The empty
		// type name uses a specific name in the generated code, such that
		// it can be identified for marshalling.
		return &MappedType{NativeType: ygot.EmptyTypeName, ZeroValue: goZeroValues[ygot.EmptyTypeName]}, nil
	case yang.Ystring:
		return &MappedType{NativeType: "string", ZeroValue: goZeroValues["string"], DefaultValue: defVal}, nil
	case yang.Yunion:
		// A YANG Union is a leaf that can take multiple values - its subtypes need
		// to be extracted.
		return s.goUnionType(args, compressOCPaths, skipEnumDedup, shortenEnumLeafNames)
	case yang.Yenum:
		// Enumeration types need to be resolved to a particular data path such
		// that a created enumered Go type can be used to set their value. Hand
		// the leaf to the enumName function to determine the name.
		if args.contextEntry == nil {
			return nil, fmt.Errorf("cannot map enum without context")
		}
		// TODO(robjs): Change this to be a call to lookup.
		mtype, err := s.enumSet.LookupEnum(args.yangType, args.contextEntry)
		//n, err := s.enumSet.enumName(args.contextEntry)
		if err != nil {
			return nil, err
		}
		if defVal != nil {
			mtype.DefaultValue = enumDefaultValue(mtype.NativeType, *defVal, goEnumPrefix)
		}
		mtype.ZeroValue = "0"
		return mtype, nil
		/*return &MappedType{
			NativeType:        fmt.Sprintf("E_%s", n),
			IsEnumeratedValue: true,
			ZeroValue:         "0",
			DefaultValue:      defVal,
		}, nil*/
	case yang.Yidentityref:
		// Identityref leaves are mapped according to the base identity that they
		// refer to - this is stored in the IdentityBase field of the context leaf
		// which is determined by the identityrefBaseTypeFromLeaf.
		if args.contextEntry == nil {
			return nil, fmt.Errorf("cannot map identityref without context")
		}
		/*n, err := s.enumSet.identityrefBaseTypeFromLeaf(args.contextEntry)
		if err != nil {
			return nil, err
		}
		if defVal != nil {
			defVal = enumDefaultValue(n, *defVal, "")
		}
		return &MappedType{
			NativeType:        fmt.Sprintf("E_%s", n),
			IsEnumeratedValue: true,
			ZeroValue:         "0",
			DefaultValue:      defVal,
		}, nil*/
		//n, err := s.enumSet.identityrefBaseTypeFromLeaf(args.contextEntry)
		//mtype := &MappedType{NativeType: n, IsEnumeratedValue: true}
		mtype, err := s.enumSet.LookupIdentity(args.contextEntry.Type.IdentityBase)
		if err != nil {
			return nil, err
		}
		if defVal != nil {
			mtype.DefaultValue = enumDefaultValue(mtype.NativeType, *defVal, goEnumPrefix)
		}
		mtype.ZeroValue = "0"
		return mtype, nil
	case yang.Ydecimal64:
		return &MappedType{NativeType: "float64", ZeroValue: goZeroValues["float64"]}, nil
	case yang.Yleafref:
		// This is a leafref, so we check what the type of the leaf that it
		// references is by looking it up in the schematree.
		target, err := s.schematree.resolveLeafrefTarget(args.yangType.Path, args.contextEntry)
		if err != nil {
			return nil, err
		}
		mtype, err = s.yangTypeToGoType(resolveTypeArgs{yangType: target.Type, contextEntry: target}, compressOCPaths, skipEnumDedup, shortenEnumLeafNames)
		if err != nil {
			return nil, err
		}
		return mtype, nil
	case yang.Ybinary:
		// Map binary fields to the Binary type defined in the output code,
		// this is used to ensure that we can distinguish a binary field from
		// a leaf-list of uint8s which is not possible if mapping to []byte.
		return &MappedType{NativeType: ygot.BinaryTypeName, ZeroValue: goZeroValues[ygot.BinaryTypeName], DefaultValue: defVal}, nil
	default:
		// Return an empty interface for the types that we do not currently
		// support. Back-end validation is required for these types.
		// TODO(robjs): Missing types currently bits. These
		// should be added.
		return &MappedType{NativeType: "interface{}", ZeroValue: goZeroValues["interface{}"]}, nil
	}
}

// goUnionType maps a YANG union to a set of Go types that should be used to
// represent it. In the simple case that the union contains only one
// subtype - e.g., is a union of string, string then the single type that
// is contained is returned as the type to be used in the generated Go code.
// This situation is common in cases that there are two strings that have
// different patterns (e.g., inet:ip-address defines two strings one matching
// the IPv4 address regexp, and the other IPv6).
//
// In the more complex case that the union consists of multiple types (e.g.,
// string, int8) then the type that is returned corresponds to a new type
// which directly relates to the path of the element. This type is intended to
// be mapped to an interface which can be implemented for each sub-type.
//
// For example:
//	container bar {
//		leaf foo {
//			type union {
//				type string;
//				type int8;
//			}
//		}
//	}
//
// Is returned with a goType of Bar_Foo_Union (where Bar_Foo is the schema
// path to an element). The unionTypes are specified to be string and int8.
//
// The compressOCPaths argument specifies whether OpenConfig path compression
// is enabled such that the name of enumerated types can be calculated correctly.
//
// The skipEnumDedup argument specifies whether the code generation should aim
// to use a common type for enumerations that are logically defined once in the schema
// but used in multiple places.
//
// goUnionType returns an error if mapping is not possible.
func (s *goGenState) goUnionType(args resolveTypeArgs, compressOCPaths, skipEnumDedup, shortenEnumLeafNames bool) (*MappedType, error) {
	var errs []error
	unionMappedTypes := make(map[int]*MappedType)

	// Extract the subtypes that are defined into a map which is keyed on the
	// mapped type. A map is used such that other functions that rely checking
	// whether a particular type is valid when creating mapping code can easily
	// check, rather than iterating the slice of strings.
	unionTypes := make(map[string]int)
	for _, subtype := range args.yangType.Type {
		errs = append(errs, s.goUnionSubTypes(subtype, args.contextEntry, unionTypes, unionMappedTypes, compressOCPaths, skipEnumDedup, shortenEnumLeafNames)...)
	}

	if errs != nil {
		return nil, fmt.Errorf("errors mapping element: %v", errs)
	}

	resolvedType := &MappedType{
		NativeType: fmt.Sprintf("%s_Union", pathToCamelCaseName(args.contextEntry, compressOCPaths, false)),
		// Zero value is set to nil, other than in cases where there is
		// a single type in the union.
		ZeroValue: "nil",
	}
	// If there is only one type inside the union, then promote it to replace the union type.
	if len(unionMappedTypes) == 1 {
		resolvedType = unionMappedTypes[0]
	}

	resolvedType.UnionTypes = unionTypes

	return resolvedType, nil
}

// goUnionSubTypes extracts all the possible subtypes of a YANG union leaf,
// returning any errors that occur. In case of nested unions, the entire union
// is flattened, and identical types are de-duped. currentTypes keeps track of
// this unique set of types, along with the order they're seen, and
// unionMappedTypes records the entire type information for each. The
// compressOCPaths argument specifies whether OpenConfig path compression is
// enabled such that the name of enumerated types can be correctly calculated.
// The skipEnumDedup argument specifies whether the current code generation is
// de-duplicating enumerations where they are used in more than one place in
// the schema.
func (s *goGenState) goUnionSubTypes(subtype *yang.YangType, ctx *yang.Entry, currentTypes map[string]int, unionMappedTypes map[int]*MappedType, compressOCPaths, skipEnumDedup, shortenEnumLeafNames bool) []error {
	var errs []error
	// If subtype.Type is not empty then this means that this type is defined to
	// be a union itself.
	if subtype.Type != nil {
		for _, st := range subtype.Type {
			errs = append(errs, s.goUnionSubTypes(st, ctx, currentTypes, unionMappedTypes, compressOCPaths, skipEnumDedup, shortenEnumLeafNames)...)
		}
		return errs
	}

	var mtype *MappedType
	switch subtype.Kind {
	case yang.Yidentityref:
		// Handle the specific case that the context entry is now not the correct entry
		// to map enumerated types to their module. This occurs in the case that the subtype
		// is an identityref - in this case, the context entry that we are carrying is the
		// leaf that refers to the union, not the specific subtype that is now being examined.
		var err error
		mtype, err = s.enumSet.LookupIdentity(subtype.IdentityBase)
		if err != nil {
			return append(errs, err)
		}
		/*baseType, err := s.enumSet.identityrefBaseTypeFromIdentity(subtype.IdentityBase)
		if err != nil {
			return append(errs, err)
		}*/
		defVal := genutil.TypeDefaultValue(subtype)
		if defVal != nil {
			defVal = enumDefaultValue(mtype.NativeType, *defVal, goEnumPrefix)
		}
		mtype.ZeroValue = "0"
		mtype.DefaultValue = defVal
		/*mtype = &MappedType{
			NativeType:        fmt.Sprintf("E_%s", baseType),
			IsEnumeratedValue: true,
			ZeroValue:         "0",
			DefaultValue:      defVal,
		}*/
	default:
		var err error

		mtype, err = s.yangTypeToGoType(resolveTypeArgs{yangType: subtype, contextEntry: ctx}, compressOCPaths, skipEnumDedup, shortenEnumLeafNames)
		if err != nil {
			errs = append(errs, err)
			return errs
		}
	}

	// Only append the type if it not one that is currently in the
	// list. To map the structure we don't care if there are two
	// typedefs that are strings underneath, as the Go code will
	// simply represent this as one string.
	if _, ok := currentTypes[mtype.NativeType]; !ok {
		index := len(currentTypes)
		currentTypes[mtype.NativeType] = index
		unionMappedTypes[index] = mtype
	}
	return errs
}
