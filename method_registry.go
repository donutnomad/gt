// Package gt provides a registry for package functions and method expressions.
// It allows registering callables by unique name and executing them with injected instances.
package gt

import (
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"reflect"
	"slices"
	"strings"
	"sync"
)

var (
	ErrEmptyName                 = errors.New("empty name")
	ErrNotFunction               = errors.New("not a function")
	ErrCallableAlreadyRegistered = errors.New("callable already registered")
	ErrNilInstance               = errors.New("nil instance")
	ErrInstanceAlreadyInjected   = errors.New("instance already injected")
	ErrInstanceNotInjected       = errors.New("instance not injected")
	ErrInvalidReturnType         = errors.New("invalid return type")
	ErrCallableNotFound          = errors.New("callable not found")
	ErrWrongArgumentCount        = errors.New("wrong argument count")
	ErrArgumentAdaptation        = errors.New("argument adaptation failed")
	ErrExecutePanic              = errors.New("execute panic")
)

type ReturnTypeChecker func([]reflect.Type) error

type Metadata map[string]any

type CallableInfo struct {
	Name         string
	Metadata     Metadata
	ReceiverType reflect.Type
	InputTypes   []reflect.Type
	OutputTypes  []reflect.Type
}

// MethodInfo stores information about a registered callable and its injection behavior.
type MethodInfo struct {
	method       reflect.Value
	metadata     Metadata
	receiverType reflect.Type
	inputTypes   []reflect.Type
	outputTypes  []reflect.Type
}

// Registry stores registered callables by name and injected instances.
type Registry struct {
	mu                sync.RWMutex
	callables         map[string]*MethodInfo         // 存储可调用对象信息和注入策略
	instances         map[reflect.Type]reflect.Value // 按类型存储注入的实例对象
	returnTypeChecker ReturnTypeChecker
}

// NewRegistry creates a new Registry instance.
func NewRegistry() *Registry {
	return &Registry{
		callables: make(map[string]*MethodInfo),
		instances: make(map[reflect.Type]reflect.Value),
	}
}

// SetReturnTypeChecker configures an optional checker for callable return types.
// When set, Register validates the callable's return signature before storing it.
func (r *Registry) SetReturnTypeChecker(checker ReturnTypeChecker) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.returnTypeChecker = checker
}

// InjectInstance injects an instance object that can be used by registered methods.
// The instance type is automatically detected and stored for later use.
func (r *Registry) InjectInstance(instance any) error {
	instanceVal := reflect.ValueOf(instance)
	if !instanceVal.IsValid() {
		return fmt.Errorf("%w", ErrNilInstance)
	}
	if instanceVal.Kind() == reflect.Ptr && instanceVal.IsNil() {
		return fmt.Errorf("%w", ErrNilInstance)
	}
	instanceType := instanceVal.Type()

	r.mu.Lock()
	defer r.mu.Unlock()

	if _, exists := r.instances[instanceType]; exists {
		return fmt.Errorf("%w: type %v", ErrInstanceAlreadyInjected, instanceType)
	}

	r.instances[instanceType] = instanceVal
	return nil
}

// Register registers a callable by its unique name.
// Any function value can be registered, including package functions, closures,
// and method expressions such as (*Book).AddBook.
//
// If Execute is called with one fewer argument than the callable expects,
// the missing first argument is resolved from injected instances by exact type.
// When no matching instance exists, Execute returns ErrInstanceNotInjected.
func (r *Registry) Register(name string, callable any, metadata ...Metadata) error {
	if name == "" {
		return fmt.Errorf("%w", ErrEmptyName)
	}

	callableVal := reflect.ValueOf(callable)
	if callableVal.Kind() != reflect.Func {
		return fmt.Errorf("%w", ErrNotFunction)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.callables[name]; exists {
		return fmt.Errorf("%w: name %q", ErrCallableAlreadyRegistered, name)
	}
	if r.returnTypeChecker != nil {
		outTypes := make([]reflect.Type, callableVal.Type().NumOut())
		for i := range outTypes {
			outTypes[i] = callableVal.Type().Out(i)
		}
		if err := r.returnTypeChecker(outTypes); err != nil {
			return fmt.Errorf("%w: %v", ErrInvalidReturnType, err)
		}
	}

	var receiverType reflect.Type
	inputTypes := make([]reflect.Type, callableVal.Type().NumIn())
	for i := range inputTypes {
		inputTypes[i] = callableVal.Type().In(i)
	}
	outputTypes := make([]reflect.Type, callableVal.Type().NumOut())
	for i := range outputTypes {
		outputTypes[i] = callableVal.Type().Out(i)
	}
	if callableVal.Type().NumIn() > 0 {
		receiverType = callableVal.Type().In(0)
	}

	copiedMetadata := Metadata{}
	if len(metadata) > 0 && metadata[0] != nil {
		copiedMetadata = maps.Clone(metadata[0])
	}

	r.callables[name] = &MethodInfo{
		method:       callableVal,
		metadata:     copiedMetadata,
		receiverType: receiverType,
		inputTypes:   inputTypes,
		outputTypes:  outputTypes,
	}
	return nil
}

// getReflectValue converts an any to reflect.Value.
// If the interface contains a reflect.Value (from calling reflect.ValueOf on a reflect.Value),
// it extracts the inner value using Interface().(reflect.Value).
func getReflectValue(arg any) reflect.Value {
	if arg == nil {
		return reflect.Value{}
	}
	if rv, ok := arg.(reflect.Value); ok {
		return rv
	}
	return reflect.ValueOf(arg)
}

var byteSliceType = reflect.TypeOf([]byte(nil))

func adaptArgValue(arg reflect.Value, targetType reflect.Type) (reflect.Value, error) {
	if !arg.IsValid() {
		switch targetType.Kind() {
		case reflect.Interface, reflect.Ptr, reflect.Map, reflect.Slice, reflect.Func, reflect.Chan:
			return reflect.Zero(targetType), nil
		default:
			return reflect.Value{}, fmt.Errorf("cannot use nil as %v", targetType)
		}
	}

	if arg.Type().AssignableTo(targetType) {
		return arg, nil
	}
	if arg.Type().ConvertibleTo(targetType) {
		return arg.Convert(targetType), nil
	}

	if arg.Type() == byteSliceType {
		decoded := reflect.New(targetType)
		if err := json.Unmarshal(arg.Bytes(), decoded.Interface()); err != nil {
			return reflect.Value{}, fmt.Errorf("json unmarshal into %v failed: %w", targetType, err)
		}
		return decoded.Elem(), nil
	}

	return reflect.Value{}, fmt.Errorf("cannot use argument of type %v as %v", arg.Type(), targetType)
}

// Execute calls the registered callable by name with arguments.
// When the first argument is omitted, Execute resolves it from injected instances by exact type.
// args can be either concrete values or reflect.Values.
// Return values are converted from reflection values to plain Go values.
func (r *Registry) Execute(name string, args ...any) ([]any, error) {
	r.mu.RLock()
	methodInfo, exists := r.callables[name]
	if !exists {
		r.mu.RUnlock()
		return nil, fmt.Errorf("%w: name %q is not registered", ErrCallableNotFound, name)
	}
	r.mu.RUnlock()

	// Convert args to reflect.Values
	argValues := make([]reflect.Value, len(args))
	for i, arg := range args {
		argValues[i] = getReflectValue(arg)
	}

	var callArgs []reflect.Value
	callArgs = argValues

	if len(methodInfo.inputTypes) > 0 && len(argValues)+1 == len(methodInfo.inputTypes) {
		instance, ok := r.injectedInstance(methodInfo.receiverType)
		if !ok {
			return nil, fmt.Errorf("%w: type %v", ErrInstanceNotInjected, methodInfo.receiverType)
		}
		callArgs = append([]reflect.Value{instance}, argValues...)
	}

	if len(callArgs) != len(methodInfo.inputTypes) {
		return nil, fmt.Errorf("%w: expected %d, got %d", ErrWrongArgumentCount, len(methodInfo.inputTypes), len(callArgs))
	}

	for i := range callArgs {
		adaptedArg, err := adaptArgValue(callArgs[i], methodInfo.inputTypes[i])
		if err != nil {
			return nil, fmt.Errorf("%w: argument %d: %v", ErrArgumentAdaptation, i, err)
		}
		callArgs[i] = adaptedArg
	}

	var rawResults []reflect.Value
	var panicValue any
	func() {
		defer func() {
			if recovered := recover(); recovered != nil {
				panicValue = recovered
				rawResults = nil
			}
		}()
		rawResults = methodInfo.method.Call(callArgs)
	}()
	if rawResults == nil {
		if panicValue != nil {
			return nil, fmt.Errorf("%w: callable %q panicked: %v", ErrExecutePanic, name, panicValue)
		}
		return nil, fmt.Errorf("%w: callable %q panicked", ErrExecutePanic, name)
	}
	results := make([]any, len(rawResults))
	for i, result := range rawResults {
		results[i] = result.Interface()
	}
	return results, nil
}

func (r *Registry) injectedInstance(receiverType reflect.Type) (reflect.Value, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	if instance, ok := r.instances[receiverType]; ok {
		return instance, true
	}

	if receiverType != nil && receiverType.Kind() != reflect.Ptr {
		ptrType := reflect.PointerTo(receiverType)
		if instance, ok := r.instances[ptrType]; ok {
			return instance.Elem(), true
		}
	}

	return reflect.Value{}, false
}

// MustExecute is like Execute but panics on error. Useful for initialization code.
func (r *Registry) MustExecute(name string, args ...any) []any {
	results, err := r.Execute(name, args...)
	if err != nil {
		panic(err)
	}
	return results
}

// GetCallable returns the raw reflect.Value for a registered name.
func (r *Registry) GetCallable(name string) (reflect.Value, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	info, exists := r.callables[name]
	if !exists {
		return reflect.Value{}, false
	}
	return info.method, true
}

// Remove unregisters a callable by name.
func (r *Registry) Remove(name string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.callables, name)
}

// Clear removes all registered callables.
func (r *Registry) Clear() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.callables = make(map[string]*MethodInfo)
}

// List returns all registered callables with their metadata and receiver type.
func (r *Registry) List() []CallableInfo {
	r.mu.RLock()
	defer r.mu.RUnlock()
	items := make([]CallableInfo, 0, len(r.callables))
	for name, info := range r.callables {
		items = append(items, CallableInfo{
			Name:         name,
			Metadata:     maps.Clone(info.metadata),
			ReceiverType: info.receiverType,
			InputTypes:   slices.Clone(info.inputTypes),
			OutputTypes:  slices.Clone(info.outputTypes),
		})
	}
	return items
}

// ListByReceiverTypes returns registered callables whose first parameter type matches any of the provided values' types.
func (r *Registry) ListByReceiverTypes(values ...any) []CallableInfo {
	r.mu.RLock()
	defer r.mu.RUnlock()

	if len(values) == 0 {
		return nil
	}

	allowed := make(map[reflect.Type]struct{}, len(values))
	for _, value := range values {
		typ := reflect.TypeOf(value)
		if typ != nil {
			allowed[typ] = struct{}{}
		}
	}

	items := make([]CallableInfo, 0, len(r.callables))
	for name, info := range r.callables {
		if _, ok := allowed[info.receiverType]; !ok {
			continue
		}
		items = append(items, CallableInfo{
			Name:         name,
			Metadata:     maps.Clone(info.metadata),
			ReceiverType: info.receiverType,
			InputTypes:   slices.Clone(info.inputTypes),
			OutputTypes:  slices.Clone(info.outputTypes),
		})
	}
	return items
}

var errorType = reflect.TypeOf((*error)(nil)).Elem()
var marshalerType = reflect.TypeOf((*json.Marshaler)(nil)).Elem()
var unmarshalerType = reflect.TypeOf((*json.Unmarshaler)(nil)).Elem()

// NetworkReturnTypeChecker validates that return types are suitable for transport-oriented usage.
func NetworkReturnTypeChecker(types []reflect.Type) error {
	for _, typ := range types {
		if err := validateNetworkReturnType(typ); err != nil {
			return err
		}
	}
	return nil
}

func validateNetworkReturnType(typ reflect.Type) error {
	return validateNetworkReturnTypeWithOptions(typ, true)
}

func validateNetworkReturnTypeWithOptions(typ reflect.Type, allowTopLevelError bool) error {
	base := typ
	if typ.Kind() == reflect.Ptr {
		base = typ.Elem()
	}

	if typ == byteSliceType {
		return nil
	}

	if base.Kind() == reflect.Slice {
		if err := validateNetworkReturnTypeWithOptions(base.Elem(), false); err != nil {
			return fmt.Errorf("slice element type %v is not supported: %w", base.Elem(), err)
		}
		return nil
	}

	if base.Kind() == reflect.Map {
		if base.Key().Kind() != reflect.String {
			return fmt.Errorf("map key type %v is not supported", base.Key())
		}
		if err := validateNetworkReturnTypeWithOptions(base.Elem(), false); err != nil {
			return fmt.Errorf("map value type %v is not supported: %w", base.Elem(), err)
		}
		return nil
	}

	switch base.Kind() {
	case reflect.Bool,
		reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64,
		reflect.Float32, reflect.Float64,
		reflect.String:
		return nil
	}

	if allowTopLevelError && typ == errorType {
		return nil
	}

	if base.Kind() == reflect.Struct {
		if implementsJSONMarshaling(base) {
			return nil
		}
		if err := validateNaturalType(base, map[reflect.Type]bool{}); err == nil {
			return nil
		}
		return fmt.Errorf("type %v must implement json.Marshaler and json.Unmarshaler", typ)
	}

	return fmt.Errorf("type %v is not supported", typ)
}

func implementsJSONMarshaling(typ reflect.Type) bool {
	ptrType := reflect.PointerTo(typ)
	marshalOK := typ.Implements(marshalerType) || ptrType.Implements(marshalerType)
	unmarshalOK := typ.Implements(unmarshalerType) || ptrType.Implements(unmarshalerType)
	return marshalOK && unmarshalOK
}

func validateNaturalType(typ reflect.Type, visiting map[reflect.Type]bool) error {
	for typ.Kind() == reflect.Ptr {
		typ = typ.Elem()
	}

	if implementsJSONMarshaling(typ) {
		return nil
	}

	switch typ.Kind() {
	case reflect.Bool,
		reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64,
		reflect.Float32, reflect.Float64,
		reflect.String:
		return nil
	case reflect.Slice:
		return validateNaturalType(typ.Elem(), visiting)
	case reflect.Struct:
		if visiting[typ] {
			return fmt.Errorf("cyclic type %v is not supported", typ)
		}
		visiting[typ] = true
		defer delete(visiting, typ)
		for i := 0; i < typ.NumField(); i++ {
			field := typ.Field(i)
			if jsonTagIgnored(field) {
				continue
			}
			if !field.IsExported() {
				return fmt.Errorf("field %s of %v is not exported", field.Name, typ)
			}
			if err := validateNaturalType(field.Type, visiting); err != nil {
				return err
			}
		}
		return nil
	default:
		return fmt.Errorf("type %v is not a natural transport type", typ)
	}
}

func jsonTagIgnored(field reflect.StructField) bool {
	tag := field.Tag.Get("json")
	if tag == "" {
		return false
	}
	name := strings.Split(tag, ",")[0]
	return name == "-"
}
