package jsonapi

import (
	"encoding/json"
	"fmt"
	"io"
	"reflect"
	"strings"
	"time"
)

type BadJSONAPIStructTag struct {
	fieldTypeName string
}

func (e BadJSONAPIStructTag) Error() string {
	return fmt.Sprintf("jsonapi tag, on %s, had too few arguments", e.fieldTypeName)
}

// MarshalOnePayload writes a jsonapi response with one, with related records sideloaded, into "included" array.
// This method encodes a response for a single record only. Hence, data will be a single record rather
// than an array of records.  If you want to serialize many records, see, MarshalManyPayload.
//
// See UnmarshalPayload for usage example.
//
// model interface{} should be a pointer to a struct.
func MarshalOnePayload(w io.Writer, model interface{}) error {
	payload, err := MarshalOne(model)
	if err != nil {
		return err
	}

	if err := json.NewEncoder(w).Encode(payload); err != nil {
		return err
	}

	return nil
}

// MarshalOne does the same as MarshalOnePayload except it just returns the payload
// and doesn't write out results.
// Useful is you use your JSON rendering library.
func MarshalOne(model interface{}) (*OnePayload, error) {
	rootNode, included, err := visitModelNode(model)
	if err != nil {
		return nil, err
	}
	payload := &OnePayload{Data: rootNode}

	payload.Included = uniqueByTypeAndID(included)

	return payload, nil
}

// MarshalManyPayload writes a jsonapi response with many records, with related records sideloaded, into "included" array.
// This method encodes a response for a slice of records, hence data will be an array of
// records rather than a single record.  To serialize a single record, see MarshalOnePayload
//
// For example you could pass it, w, your http.ResponseWriter, and, models, a slice of Blog
// struct instance pointers as interface{}'s to write to the response,
//
//	 func ListBlogs(w http.ResponseWriter, r *http.Request) {
//		 // ... fetch your blogs and filter, offset, limit, etc ...
//
//		 blogs := testBlogsForList()
//
//		 w.WriteHeader(200)
//		 w.Header().Set("Content-Type", "application/vnd.api+json")
//		 if err := jsonapi.MarshalManyPayload(w, blogs); err != nil {
//			 http.Error(w, err.Error(), 500)
//		 }
//	 }
//
//
// Visit https://github.com/shwoodard/jsonapi#list for more info.
//
// models []interface{} should be a slice of struct pointers.
func MarshalManyPayload(w io.Writer, models []interface{}) error {
	payload, err := MarshalMany(models)
	if err != nil {
		return err
	}

	if err := json.NewEncoder(w).Encode(payload); err != nil {
		return err
	}

	return nil
}

// MarshalMany does the same as MarshalManyPayload except it just returns the payload
// and doesn't write out results.
// Useful is you use your JSON rendering library.
func MarshalMany(models []interface{}) (*ManyPayload, error) {
	modelsValues := reflect.ValueOf(models)
	data := make([]*Node, 0, modelsValues.Len())

	var incl []*Node

	for i := 0; i < modelsValues.Len(); i++ {
		model := modelsValues.Index(i).Interface()

		node, included, err := visitModelNode(model)
		if err != nil {
			return nil, err
		}
		data = append(data, node)
		incl = append(incl, included...)
	}

	payload := &ManyPayload{
		Data:     data,
		Included: uniqueByTypeAndID(incl),
	}

	return payload, nil
}

func visitModelNode(model interface{}) (*Node, []*Node, error) {
	node := new(Node)

	var er error
	var included []*Node
	var sideload bool

	modelType := reflect.TypeOf(model).Elem()
	modelValue := reflect.ValueOf(model).Elem()

	var i = 0
	modelType.FieldByNameFunc(func(name string) bool {
		if er != nil {
			return false
		}

		structField := modelType.Field(i)
		tag := structField.Tag.Get("jsonapi")
		if tag == "" {
			i++
			return false
		}

		fieldValue := modelValue.Field(i)

		i++

		args := strings.Split(tag, ",")

		if len(args) < 1 {
			er = BadJSONAPIStructTag{structField.Name}
			return false
		}

		annotation := args[0]

		if (annotation == "client-id" && len(args) != 1) || (annotation != "client-id" && len(args) < 2) {
			er = BadJSONAPIStructTag{structField.Name}
			return false
		}

		if len(args) == 3 && args[2] == "include" {
			sideload = true
		} else {
			sideload = false
		}

		if annotation == "primary" {
			node.Id = fmt.Sprintf("%v", fieldValue.Interface())
			node.Type = args[1]
		} else if annotation == "client-id" {
			node.ClientId = fieldValue.String()
		} else if annotation == "attr" {
			if node.Attributes == nil {
				node.Attributes = make(map[string]interface{})
			}

			if fieldValue.Type() == reflect.TypeOf(time.Time{}) {
				isZeroMethod := fieldValue.MethodByName("IsZero")
				isZero := isZeroMethod.Call(make([]reflect.Value, 0))[0].Interface().(bool)
				if isZero {
					return false
				}

				unix := fieldValue.MethodByName("Unix")
				val := unix.Call(make([]reflect.Value, 0))[0]
				node.Attributes[args[1]] = val.Int()
			} else {
				node.Attributes[args[1]] = fieldValue.Interface()
			}
		} else if annotation == "relation" {
			isSlice := fieldValue.Type().Kind() == reflect.Slice

			if (isSlice && fieldValue.Len() < 1) || (!isSlice && fieldValue.IsNil()) {
				return false
			}

			if node.Relationships == nil {
				node.Relationships = make(map[string]interface{})
			}

			if sideload && included == nil {
				included = make([]*Node, 0)
			}

			if isSlice {
				relationship, incl, err := visitModelNodeRelationships(args[1], fieldValue, sideload)

				if err == nil {
					if sideload {
						included = append(included, incl...)
					}

					d := relationship.Data
					var shallowNodes []*Node
					for _, node := range d {
						shallowNodes = append(shallowNodes, toShallowNode(node))
					}

					node.Relationships[args[1]] = &RelationshipManyNode{Data: shallowNodes}
				} else {
					er = err
					return false
				}
			} else {
				relationship, incl, err := visitModelNode(fieldValue.Interface())
				if err == nil {
					if sideload {
						included = append(included, incl...)
						included = append(included, relationship)
					}
					node.Relationships[args[1]] = &RelationshipOneNode{Data: toShallowNode(relationship)}
				} else {
					er = err
					return false
				}
			}

		} else {
			er = fmt.Errorf("Unsupported jsonapi tag annotation, %s", annotation)
			return false
		}

		return false
	})

	if er != nil {
		return nil, nil, er
	}

	return node, included, nil
}

func toShallowNode(node *Node) *Node {
	return &Node{
		Id:   node.Id,
		Type: node.Type,
	}
}

func visitModelNodeRelationships(relationName string, models reflect.Value, sideload bool) (*RelationshipManyNode, []*Node, error) {
	var nodes []*Node
	var included []*Node

	if sideload {
		included = make([]*Node, 0)
	}

	if models.Len() == 0 {
		nodes = make([]*Node, 0)
	}

	for i := 0; i < models.Len(); i++ {
		node, incl, err := visitModelNode(models.Index(i).Interface())
		if err != nil {
			return nil, nil, err
		}

		nodes = append(nodes, node)
		included = append(included, incl...)
	}

	included = append(included, nodes...)

	n := &RelationshipManyNode{Data: nodes}

	return n, included, nil
}

func uniqueByTypeAndID(nodes []*Node) []*Node {
	uniqueIncluded := make(map[string]*Node)

	for i := 0; i < len(nodes); i++ {
		n := nodes[i]
		k := fmt.Sprintf("%s,%s", n.Type, n.Id)
		if uniqueIncluded[k] == nil {
			uniqueIncluded[k] = n
		} else {
			nodes = append(nodes[:i], nodes[i+1:]...)
			i--
		}
	}

	return nodes
}
