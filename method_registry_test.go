package gt

import (
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"reflect"
	"strings"
	"sync"
	"testing"
)

type Book struct {
	Title  string
	Author string
}

func (b *Book) AddBook(title, author string) string {
	b.Title = title
	b.Author = author
	return "Book added: " + title + " by " + author
}

func (b Book) Describe(suffix string) string {
	return b.Title + suffix
}

func AddBookFunc(title, author string) string {
	return "Function: Book added: " + title + " by " + author
}

type MathOps struct{}

func (m *MathOps) Add(a, b int) int {
	return a + b
}

type Car struct {
	Model string
}

func (c *Car) Drive(speed int) string {
	return "Driving " + c.Model + " at " + fmt.Sprint(speed) + " km/h"
}

func Test2(t *testing.T) {
	reg := NewRegistry()

	// 注册包函数
	err := reg.Register("addBookFunc", AddBookFunc)
	if err != nil {
		panic(err)
	}

	// 注入实例对象 - 系统自动识别类型
	book := &Book{}
	err = reg.InjectInstance(book)
	if err != nil {
		panic(err)
	}

	// 注册方法表达式 - 自动关联到相同类型的注入实例
	err = reg.Register("addBook", (*Book).AddBook)
	if err != nil {
		panic(err)
	}

	// 执行包函数 - 不需要实例
	result, err := reg.Execute("addBookFunc", "The Great Gatsby", "F. Scott Fitzgerald")
	if err != nil {
		panic(err)
	}
	fmt.Println("包函数结果:", result[0].(string))

	// 执行方法 - 自动使用注入的Book实例
	results, err := reg.Execute("addBook", "1984", "George Orwell")
	if err != nil {
		panic(err)
	}
	fmt.Println("方法结果:", results[0].(string))
	fmt.Printf("Book实例被修改: Title=%q, Author=%q\n", book.Title, book.Author)
}

func TestRegistry(t *testing.T) {
	reg := NewRegistry()

	err := reg.Register("addBookFunc", AddBookFunc)
	if err != nil {
		t.Fatal("Failed to register function:", err)
	}

	book := &Book{}
	err = reg.InjectInstance(book)
	if err != nil {
		t.Fatal("Failed to inject instance:", err)
	}

	err = reg.Register("addBook", (*Book).AddBook)
	if err != nil {
		t.Fatal("Failed to register method:", err)
	}

	result, err := reg.Execute("addBookFunc",
		reflect.ValueOf("The Great Gatsby"),
		reflect.ValueOf("F. Scott Fitzgerald"))
	if err != nil {
		t.Fatal("Execute failed:", err)
	}
	if len(result) > 0 && result[0].(string) != "Function: Book added: The Great Gatsby by F. Scott Fitzgerald" {
		t.Error("Unexpected result:", result[0])
	}

	results, err := reg.Execute("addBook",
		reflect.ValueOf("1984"),
		reflect.ValueOf("George Orwell"))
	if err != nil {
		t.Fatal("Execute method failed:", err)
	}
	if book.Title != "1984" || book.Author != "George Orwell" {
		t.Error("Book instance was not modified correctly")
	}
	if len(results) > 0 && results[0].(string) != "Book added: 1984 by George Orwell" {
		t.Error("Unexpected method result:", results[0])
	}

	results2 := reg.MustExecute("addBookFunc",
		reflect.ValueOf("To Kill a Mockingbird"),
		reflect.ValueOf("Harper Lee"))
	if len(results2) > 0 && results2[0].(string) != "Function: Book added: To Kill a Mockingbird by Harper Lee" {
		t.Error("MustExecute failed:", results2[0])
	}
}

func TestRegistryWithStruct(t *testing.T) {
	reg := NewRegistry()

	car := &Car{Model: "Honda Civic"}
	err := reg.InjectInstance(car)
	if err != nil {
		t.Fatal("Failed to inject instance:", err)
	}

	err = reg.Register("driveCar", (*Car).Drive)
	if err != nil {
		t.Fatal("Failed to register Car.Drive:", err)
	}

	results, err := reg.Execute("driveCar",
		reflect.ValueOf(120))
	expected := "Driving Honda Civic at 120 km/h"
	if results[0].(string) != expected {
		t.Errorf("Expected %q, got %q", expected, results[0].(string))
	}
}

func TestMultipleMethods(t *testing.T) {
	reg := NewRegistry()
	math := &MathOps{}

	err := reg.InjectInstance(math)
	if err != nil {
		t.Fatal("Failed to inject instance:", err)
	}

	err = reg.Register("add", (*MathOps).Add)
	if err != nil {
		t.Fatal("Failed to register Add:", err)
	}

	results, err := reg.Execute("add",
		reflect.ValueOf(5),
		reflect.ValueOf(3))
	if err != nil {
		t.Fatal("Add failed:", err)
	}
	if results[0].(int) != 8 {
		t.Error("Add result incorrect:", results[0].(int))
	}

	results, err = reg.Execute("add",
		reflect.ValueOf(3),
		reflect.ValueOf(5))
	if err != nil {
		t.Fatal("Add failed:", err)
	}
	if results[0].(int) != 8 {
		t.Error("Add result incorrect:", results[0].(int))
	}
}

func TestRegisterFuncExample(t *testing.T) {
	reg := NewRegistry()

	err := reg.Register("processor", func(data string) string {
		return "Closure processed: " + data
	})
	if err != nil {
		t.Fatal("Failed to register closure:", err)
	}

	results, err := reg.Execute("processor",
		reflect.ValueOf("test data"))
	if err != nil {
		t.Fatal("Execute closure failed:", err)
	}
	expected := "Closure processed: test data"
	if results[0].(string) != expected {
		t.Errorf("Expected %q, got %q", expected, results[0].(string))
	}
}

type Database struct {
	ConnectionString string
}

type ComplexArgs struct {
	Name   string            `json:"name"`
	Age    int               `json:"age"`
	Tags   []string          `json:"tags"`
	Labels map[string]string `json:"labels"`
}

type JSONPayload struct {
	Value string `json:"value"`
}

func (p JSONPayload) MarshalJSON() ([]byte, error) {
	type alias JSONPayload
	return json.Marshal(alias(p))
}

func (p *JSONPayload) UnmarshalJSON(data []byte) error {
	type alias JSONPayload
	var out alias
	if err := json.Unmarshal(data, &out); err != nil {
		return err
	}
	*p = JSONPayload(out)
	return nil
}

type PlainPayload struct {
	Value string
}

type OpaqueJSONDecimal struct {
	raw string
}

func (d OpaqueJSONDecimal) MarshalJSON() ([]byte, error) {
	return json.Marshal(d.raw)
}

func (d *OpaqueJSONDecimal) UnmarshalJSON(data []byte) error {
	var out string
	if err := json.Unmarshal(data, &out); err != nil {
		return err
	}
	d.raw = out
	return nil
}

type NestedNaturalPayload struct {
	Label string
	Flags []bool
}

type NaturalPayload struct {
	Name   string
	Count  int
	Scores []float64
	Child  NestedNaturalPayload
}

type NaturalPayloadWithJSONField struct {
	RequiredAmount OpaqueJSONDecimal `json:"required_amount"`
}

type InvalidNestedPayload struct {
	Meta map[string]string
}

type InvalidNaturalPayload struct {
	Child InvalidNestedPayload
}

type IgnoredBigIntPayload struct {
	Name    string   `json:"name"`
	Skipped chan int `json:"-"`
}

type StructuredKey struct {
	Value string `json:"value"`
}

func (k StructuredKey) MarshalJSON() ([]byte, error) {
	type alias StructuredKey
	return json.Marshal(alias(k))
}

func (k *StructuredKey) UnmarshalJSON(data []byte) error {
	type alias StructuredKey
	var out alias
	if err := json.Unmarshal(data, &out); err != nil {
		return err
	}
	*k = StructuredKey(out)
	return nil
}

func AcceptComplexArgs(args ComplexArgs) string {
	return fmt.Sprintf("%s:%d:%s:%s", args.Name, args.Age, args.Tags[0], args.Labels["role"])
}

func (db *Database) Query(sql string) string {
	return "Query executed on " + db.ConnectionString + ": " + sql
}

func TestRegisterMethodPtrReceiver(t *testing.T) {
	reg := NewRegistry()
	db := &Database{ConnectionString: "postgresql://localhost"}

	err := reg.InjectInstance(db)
	if err != nil {
		t.Fatal("Failed to inject instance:", err)
	}

	err = reg.Register("query", (*Database).Query)
	if err != nil {
		t.Fatal("Failed to register Query method:", err)
	}

	results, err := reg.Execute("query",
		reflect.ValueOf("SELECT * FROM users"))
	expected := "Query executed on postgresql://localhost: SELECT * FROM users"
	if results[0].(string) != expected {
		t.Errorf("Expected %q, got %q", expected, results[0].(string))
	}
}

func TestRegistryManagement(t *testing.T) {
	reg := NewRegistry()

	err := reg.Register("tempFunc", func() string { return "temp" })
	if err != nil {
		t.Fatal("Failed to register:", err)
	}

	if _, exists := reg.GetCallable("tempFunc"); !exists {
		t.Error("Expected callable to exist")
	}

	reg.Remove("tempFunc")
	if _, exists := reg.GetCallable("tempFunc"); exists {
		t.Error("Expected callable to be removed")
	}

	reg.Register("func1", func() {})
	reg.Register("func2", func() {})
	list := reg.List()
	if len(list) != 2 {
		t.Error("Expected 2 registered items")
	}

	reg.Clear()
	if len(reg.List()) != 0 {
		t.Error("Expected registry to be empty after clear")
	}
}

func TestErrorCases(t *testing.T) {
	reg := NewRegistry()

	// Register duplicate name
	err := reg.Register("test", func() {})
	if err != nil {
		t.Fatal("First registration should succeed:", err)
	}

	err = reg.Register("test", func() {})
	if err == nil {
		t.Error("Expected error for duplicate registration")
	}

	// Execute non-existent
	_, err = reg.Execute("nonexistent")
	if err == nil {
		t.Error("Expected error for non-existent name")
	}

	// Execute with wrong number of arguments
	err = reg.Register("twoArgsFunc", func(a, b string) {})
	if err != nil {
		t.Fatal("Failed to register:", err)
	}

	_, err = reg.Execute("twoArgsFunc", "only one arg")
	if err == nil {
		t.Error("Expected error for wrong number of arguments")
	}
}

func TestRegistryArgsAcceptComplexStruct(t *testing.T) {
	reg := NewRegistry()
	if err := reg.Register("acceptComplexArgs", AcceptComplexArgs); err != nil {
		t.Fatal("Failed to register function:", err)
	}

	args := ComplexArgs{
		Name: "alice",
		Age:  30,
		Tags: []string{"admin"},
		Labels: map[string]string{
			"role": "owner",
		},
	}

	results, err := reg.Execute("acceptComplexArgs", args)
	if err != nil {
		t.Fatal("Execute failed:", err)
	}

	if got, want := results[0].(string), "alice:30:admin:owner"; got != want {
		t.Fatalf("expected %q, got %q", want, got)
	}
}

func TestRegistryArgsUnmarshalJSONBytesOnTypeMismatch(t *testing.T) {
	reg := NewRegistry()
	if err := reg.Register("acceptComplexArgs", AcceptComplexArgs); err != nil {
		t.Fatal("Failed to register function:", err)
	}

	payload, err := json.Marshal(ComplexArgs{
		Name: "bob",
		Age:  28,
		Tags: []string{"editor"},
		Labels: map[string]string{
			"role": "maintainer",
		},
	})
	if err != nil {
		t.Fatal("Marshal failed:", err)
	}

	results, err := reg.Execute("acceptComplexArgs", payload)
	if err == nil {
		if got, want := results[0].(string), "bob:28:editor:maintainer"; got != want {
			t.Fatalf("expected %q, got %q", want, got)
		}
		return
	}

	t.Fatalf("expected []byte JSON payload to be unmarshaled automatically, got error: %v", err)
}

func TestRegisterMethodExpressionWithoutInjectedInstanceAcceptsExplicitReceiver(t *testing.T) {
	reg := NewRegistry()

	if err := reg.Register("addBook", (*Book).AddBook); err != nil {
		t.Fatal("Failed to register method expression without instance:", err)
	}

	book := &Book{}
	results, err := reg.Execute("addBook", book, "Dune", "Frank Herbert")
	if err != nil {
		t.Fatal("Execute failed:", err)
	}

	if got, want := results[0].(string), "Book added: Dune by Frank Herbert"; got != want {
		t.Fatalf("expected %q, got %q", want, got)
	}
	if book.Title != "Dune" || book.Author != "Frank Herbert" {
		t.Fatalf("expected explicit receiver to be updated")
	}
}

func TestRegisterMethodExpressionBeforeInjectUsesInjectedInstanceAtExecuteTime(t *testing.T) {
	reg := NewRegistry()

	if err := reg.Register("addBook", (*Book).AddBook); err != nil {
		t.Fatal("Failed to register method expression:", err)
	}

	book := &Book{}
	if err := reg.InjectInstance(book); err != nil {
		t.Fatal("Failed to inject instance:", err)
	}

	results, err := reg.Execute("addBook", "Dune", "Frank Herbert")
	if err != nil {
		t.Fatal("Execute failed:", err)
	}

	if got, want := results[0].(string), "Book added: Dune by Frank Herbert"; got != want {
		t.Fatalf("expected %q, got %q", want, got)
	}
	if book.Title != "Dune" || book.Author != "Frank Herbert" {
		t.Fatalf("expected injected receiver to be updated")
	}
}

func TestExecuteReturnsSentinelErrors(t *testing.T) {
	reg := NewRegistry()

	if _, err := reg.Execute("missing"); !errors.Is(err, ErrCallableNotFound) {
		t.Fatalf("expected ErrCallableNotFound, got %v", err)
	}

	if err := reg.Register("twoArgs", func(a, b string) {}); err != nil {
		t.Fatal(err)
	}
	if _, err := reg.Execute("twoArgs", "only one"); !errors.Is(err, ErrInstanceNotInjected) {
		t.Fatalf("expected ErrInstanceNotInjected, got %v", err)
	}
	if _, err := reg.Execute("twoArgs"); !errors.Is(err, ErrWrongArgumentCount) {
		t.Fatalf("expected ErrWrongArgumentCount, got %v", err)
	}

	if err := reg.Register("needInt", func(v int) {}); err != nil {
		t.Fatal(err)
	}
	if _, err := reg.Execute("needInt", "bad"); !errors.Is(err, ErrArgumentAdaptation) {
		t.Fatalf("expected ErrArgumentAdaptation, got %v", err)
	}

	if err := reg.Register("needInjectedBook", func(b *Book, suffix string) string {
		return b.Title + suffix
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := reg.Execute("needInjectedBook", "!"); !errors.Is(err, ErrInstanceNotInjected) {
		t.Fatalf("expected ErrInstanceNotInjected, got %v", err)
	}
}

func TestRegisterAndInjectReturnSentinelErrors(t *testing.T) {
	reg := NewRegistry()

	if err := reg.Register("", func() {}); !errors.Is(err, ErrEmptyName) {
		t.Fatalf("expected ErrEmptyName, got %v", err)
	}

	if err := reg.Register("fn", func() {}); err != nil {
		t.Fatal(err)
	}
	if err := reg.Register("fn", func() {}); !errors.Is(err, ErrCallableAlreadyRegistered) {
		t.Fatalf("expected ErrCallableAlreadyRegistered, got %v", err)
	}

	if err := reg.Register("bad", 123); !errors.Is(err, ErrNotFunction) {
		t.Fatalf("expected ErrNotFunction, got %v", err)
	}

	book := &Book{}
	if err := reg.InjectInstance(book); err != nil {
		t.Fatal(err)
	}
	if err := reg.InjectInstance(book); !errors.Is(err, ErrInstanceAlreadyInjected) {
		t.Fatalf("expected ErrInstanceAlreadyInjected, got %v", err)
	}

	if err := reg.InjectInstance(nil); !errors.Is(err, ErrNilInstance) {
		t.Fatalf("expected ErrNilInstance, got %v", err)
	}

	var nilBook *Book
	if err := reg.InjectInstance(nilBook); !errors.Is(err, ErrNilInstance) {
		t.Fatalf("expected typed nil pointer to return ErrNilInstance, got %v", err)
	}
}

func TestRegisterReturnTypeChecker(t *testing.T) {
	reg := NewRegistry()

	if err := reg.Register("plainStruct", func() PlainPayload { return PlainPayload{} }); err != nil {
		t.Fatalf("default registry should not reject return type, got %v", err)
	}

	reg.SetReturnTypeChecker(NetworkReturnTypeChecker)

	if err := reg.Register("builtinTypes2", func() *big.Int {
		return nil
	}); err != nil {
		t.Fatalf("expected *big.Int to pass because it implements JSON interfaces, got %v", err)
	}

	if err := reg.Register("builtinTypes", func() (string, *int, error, []byte, bool) {
		return "", nil, nil, nil, false
	}); err != nil {
		t.Fatalf("expected builtin types to pass, got %v", err)
	}

	if err := reg.Register("pointerTypes", func() (*string, *bool, *JSONPayload) {
		return nil, nil, nil
	}); err != nil {
		t.Fatalf("expected pointer types to pass, got %v", err)
	}

	if err := reg.Register("jsonStruct", func() JSONPayload { return JSONPayload{} }); err != nil {
		t.Fatalf("expected JSONPayload to pass, got %v", err)
	}

	if err := reg.Register("plainStruct2", func() PlainPayload { return PlainPayload{} }); err != nil {
		t.Fatalf("expected PlainPayload to pass as natural struct, got %v", err)
	}

	if err := reg.Register("naturalStructWithJSONField", func() NaturalPayloadWithJSONField {
		return NaturalPayloadWithJSONField{}
	}); err != nil {
		t.Fatalf("expected NaturalPayloadWithJSONField to pass because nested field implements JSON interfaces, got %v", err)
	}

	if err := reg.Register("naturalStruct", func() NaturalPayload { return NaturalPayload{} }); err != nil {
		t.Fatalf("expected NaturalPayload to pass as nested natural struct, got %v", err)
	}

	if err := reg.Register("ignoredBigIntStruct", func() IgnoredBigIntPayload { return IgnoredBigIntPayload{} }); err != nil {
		t.Fatalf("expected IgnoredBigIntPayload to pass because json:\"-\" field should be ignored, got %v", err)
	}

	if err := reg.Register("invalidNaturalStruct", func() InvalidNaturalPayload { return InvalidNaturalPayload{} }); !errors.Is(err, ErrInvalidReturnType) {
		t.Fatalf("expected ErrInvalidReturnType for invalid nested natural struct, got %v", err)
	}

	if err := reg.Register("stringSlice", func() []string { return nil }); err != nil {
		t.Fatalf("expected []string to pass, got %v", err)
	}

	if err := reg.Register("jsonStructSlice", func() []JSONPayload { return nil }); err != nil {
		t.Fatalf("expected []JSONPayload to pass, got %v", err)
	}

	if err := reg.Register("jsonStructPtrSlice", func() []*JSONPayload { return nil }); err != nil {
		t.Fatalf("expected []*JSONPayload to pass, got %v", err)
	}

	if err := reg.Register("plainStructSlice", func() []PlainPayload { return nil }); err != nil {
		t.Fatalf("expected []PlainPayload to pass as slice of natural structs, got %v", err)
	}

	if err := reg.Register("errorSlice", func() []error { return nil }); !errors.Is(err, ErrInvalidReturnType) {
		t.Fatalf("expected ErrInvalidReturnType for []error, got %v", err)
	}

	if err := reg.Register("stringMap", func() map[string]string { return nil }); err != nil {
		t.Fatalf("expected map[string]string to pass, got %v", err)
	}

	if err := reg.Register("intMap", func() map[string]int64 { return nil }); err != nil {
		t.Fatalf("expected map[string]int64 to pass, got %v", err)
	}

	if err := reg.Register("boolMap", func() map[string]bool { return nil }); err != nil {
		t.Fatalf("expected map[string]bool to pass, got %v", err)
	}

	if err := reg.Register("floatMap", func() map[string]float64 { return nil }); err != nil {
		t.Fatalf("expected map[string]float64 to pass, got %v", err)
	}

	if err := reg.Register("stringSliceMap", func() map[string][]string { return nil }); err != nil {
		t.Fatalf("expected map[string][]string to pass, got %v", err)
	}

	if err := reg.Register("intSliceMap", func() map[string][]int32 { return nil }); err != nil {
		t.Fatalf("expected map[string][]int32 to pass, got %v", err)
	}

	if err := reg.Register("jsonStructMap", func() map[string]JSONPayload { return nil }); err != nil {
		t.Fatalf("expected map[string]JSONPayload to pass, got %v", err)
	}

	if err := reg.Register("jsonStructPtrMap", func() map[string]*JSONPayload { return nil }); err != nil {
		t.Fatalf("expected map[string]*JSONPayload to pass, got %v", err)
	}

	if err := reg.Register("plainStructMap", func() map[string]PlainPayload { return nil }); err != nil {
		t.Fatalf("expected map[string]PlainPayload to pass as map of natural structs, got %v", err)
	}

	if err := reg.Register("invalidNaturalStructSlice", func() []InvalidNaturalPayload { return nil }); !errors.Is(err, ErrInvalidReturnType) {
		t.Fatalf("expected ErrInvalidReturnType for []InvalidNaturalPayload, got %v", err)
	}

	if err := reg.Register("invalidNaturalStructMap", func() map[string]InvalidNaturalPayload { return nil }); !errors.Is(err, ErrInvalidReturnType) {
		t.Fatalf("expected ErrInvalidReturnType for map[string]InvalidNaturalPayload, got %v", err)
	}

	if err := reg.Register("errorMapValue", func() map[string]error { return nil }); !errors.Is(err, ErrInvalidReturnType) {
		t.Fatalf("expected ErrInvalidReturnType for map[string]error, got %v", err)
	}

	if err := reg.Register("errorMapKey", func() map[error]string { return nil }); !errors.Is(err, ErrInvalidReturnType) {
		t.Fatalf("expected ErrInvalidReturnType for map[error]string, got %v", err)
	}

	if err := reg.Register("intKeyMap", func() map[int]string { return nil }); !errors.Is(err, ErrInvalidReturnType) {
		t.Fatalf("expected ErrInvalidReturnType for map[int]string, got %v", err)
	}

	if err := reg.Register("structKeyMap", func() map[StructuredKey]string { return nil }); !errors.Is(err, ErrInvalidReturnType) {
		t.Fatalf("expected ErrInvalidReturnType for map[StructuredKey]string, got %v", err)
	}
}

func TestRegisterAutoBindsFirstParameterWhenInjectedTypeMatches(t *testing.T) {
	reg := NewRegistry()
	book := &Book{Title: "bound"}
	if err := reg.InjectInstance(book); err != nil {
		t.Fatal(err)
	}
	if err := reg.Register("describeBook", func(b *Book, suffix string) string {
		return b.Title + suffix
	}); err != nil {
		t.Fatal(err)
	}

	results, err := reg.Execute("describeBook", "!")
	if err != nil {
		t.Fatal(err)
	}
	if got, want := results[0].(string), "bound!"; got != want {
		t.Fatalf("expected %q, got %q", want, got)
	}
}

func TestRegisterBeforeInjectUsesInjectedInstanceAtExecuteTime(t *testing.T) {
	reg := NewRegistry()
	if err := reg.Register("describeBook", func(b *Book, suffix string) string {
		return b.Title + suffix
	}); err != nil {
		t.Fatal(err)
	}

	book := &Book{Title: "late"}
	if err := reg.InjectInstance(book); err != nil {
		t.Fatal(err)
	}

	results, err := reg.Execute("describeBook", "!")
	if err != nil {
		t.Fatal(err)
	}
	if got, want := results[0].(string), "late!"; got != want {
		t.Fatalf("expected %q, got %q", want, got)
	}
}

func TestExecuteRecoversPanicsAsError(t *testing.T) {
	reg := NewRegistry()
	if err := reg.Register("panicString", func() string {
		panic("boom")
	}); err != nil {
		t.Fatal(err)
	}

	_, err := reg.Execute("panicString")
	if !errors.Is(err, ErrExecutePanic) {
		t.Fatalf("expected ErrExecutePanic, got %v", err)
	}
	if !strings.Contains(err.Error(), "boom") {
		t.Fatalf("expected panic detail in error, got %v", err)
	}
}

func TestExecuteUsesPointerInjectionForValueReceiver(t *testing.T) {
	reg := NewRegistry()
	if err := reg.Register("describe", Book.Describe); err != nil {
		t.Fatal(err)
	}

	book := &Book{Title: "pointer"}
	if err := reg.InjectInstance(book); err != nil {
		t.Fatal(err)
	}

	results, err := reg.Execute("describe", "!")
	if err != nil {
		t.Fatal(err)
	}
	if got, want := results[0].(string), "pointer!"; got != want {
		t.Fatalf("expected %q, got %q", want, got)
	}
}

func TestExecuteConcurrentReads(t *testing.T) {
	reg := NewRegistry()
	if err := reg.Register("add", func(a, b int) int {
		return a + b
	}); err != nil {
		t.Fatal(err)
	}

	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Go(func() {
			for j := 0; j < 100; j++ {
				results, err := reg.Execute("add", 20, 22)
				if err != nil {
					t.Errorf("Execute failed: %v", err)
					return
				}
				if got := results[0].(int); got != 42 {
					t.Errorf("expected 42, got %d", got)
					return
				}
			}
		})
	}
	wg.Wait()
}

func TestRegistryListWithMetadataAndReceiverTypeFilter(t *testing.T) {
	reg := NewRegistry()

	bookMetadata := map[string]any{"name": "book-handler"}
	if err := reg.Register("bookFn", func(b *Book, title string) string {
		return b.Title + title
	}, bookMetadata); err != nil {
		t.Fatal(err)
	}
	bookMetadata["name"] = "mutated"

	if err := reg.Register("stringFn", func(s string) string { return s }, map[string]any{"name": "string-handler"}); err != nil {
		t.Fatal(err)
	}
	if err := reg.Register("noArgFn", func() string { return "ok" }); err != nil {
		t.Fatal(err)
	}
	if err := reg.Register("sigFn", func(a string, b string) (int, error) {
		return 0, nil
	}, map[string]any{"name": "sig-handler"}); err != nil {
		t.Fatal(err)
	}

	items := reg.List()
	if len(items) != 4 {
		t.Fatalf("expected 4 items, got %d", len(items))
	}

	indexByName := make(map[string]CallableInfo, len(items))
	for _, item := range items {
		indexByName[item.Name] = item
	}

	bookItem := indexByName["bookFn"]
	if got := bookItem.Metadata["name"]; got != "book-handler" {
		t.Fatalf("expected metadata copy to preserve original value, got %v", got)
	}
	if bookItem.ReceiverType != reflect.TypeFor[*Book]() {
		t.Fatalf("expected receiver type *Book, got %v", bookItem.ReceiverType)
	}
	if len(bookItem.InputTypes) != 2 || bookItem.InputTypes[0] != reflect.TypeFor[*Book]() || bookItem.InputTypes[1] != reflect.TypeFor[string]() {
		t.Fatalf("unexpected input types for bookFn: %v", bookItem.InputTypes)
	}
	if len(bookItem.OutputTypes) != 1 || bookItem.OutputTypes[0] != reflect.TypeFor[string]() {
		t.Fatalf("unexpected output types for bookFn: %v", bookItem.OutputTypes)
	}

	noArgItem := indexByName["noArgFn"]
	if noArgItem.ReceiverType != nil {
		t.Fatalf("expected noArgFn to have nil receiver type, got %v", noArgItem.ReceiverType)
	}
	if len(noArgItem.Metadata) != 0 {
		t.Fatalf("expected empty metadata for noArgFn, got %v", noArgItem.Metadata)
	}
	if len(noArgItem.InputTypes) != 0 {
		t.Fatalf("expected no input types for noArgFn, got %v", noArgItem.InputTypes)
	}
	if len(noArgItem.OutputTypes) != 1 || noArgItem.OutputTypes[0] != reflect.TypeFor[string]() {
		t.Fatalf("unexpected output types for noArgFn: %v", noArgItem.OutputTypes)
	}

	sigItem := indexByName["sigFn"]
	if len(sigItem.InputTypes) != 2 || sigItem.InputTypes[0] != reflect.TypeFor[string]() || sigItem.InputTypes[1] != reflect.TypeFor[string]() {
		t.Fatalf("unexpected input types for sigFn: %v", sigItem.InputTypes)
	}
	if len(sigItem.OutputTypes) != 2 || sigItem.OutputTypes[0] != reflect.TypeFor[int]() || sigItem.OutputTypes[1] != reflect.TypeFor[error]() {
		t.Fatalf("unexpected output types for sigFn: %v", sigItem.OutputTypes)
	}

	filtered := reg.ListByReceiverTypes(&Book{}, "")
	if len(filtered) != 3 {
		t.Fatalf("expected 3 filtered items, got %d", len(filtered))
	}

	filteredNames := map[string]bool{}
	for _, item := range filtered {
		filteredNames[item.Name] = true
	}
	if !filteredNames["bookFn"] || !filteredNames["stringFn"] || !filteredNames["sigFn"] {
		t.Fatalf("unexpected filtered result: %v", filteredNames)
	}
	if filteredNames["noArgFn"] {
		t.Fatalf("did not expect noArgFn in filtered result")
	}

	filteredByValue := reg.ListByReceiverTypes(Book{})
	if len(filteredByValue) != 0 {
		t.Fatalf("expected no matches for Book{} value receiver type, got %d", len(filteredByValue))
	}

	var nilBook *Book
	filteredByTypedNil := reg.ListByReceiverTypes(nilBook)
	if len(filteredByTypedNil) != 1 || filteredByTypedNil[0].Name != "bookFn" {
		t.Fatalf("expected typed nil pointer to match bookFn, got %v", filteredByTypedNil)
	}
}
