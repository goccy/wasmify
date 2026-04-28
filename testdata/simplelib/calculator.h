#pragma once

#include <string>
#include <vector>

namespace calc {

// Simple enum for operations
enum class Operation {
    Add = 0,
    Subtract = 1,
    Multiply = 2,
    Divide = 3,
};

// AnchorEnums mimics the shape that protobuf-generated headers use:
// the underlying enumerators live at file/class scope with mangled
// names, then a wrapper class re-exports them via a typedef + an
// `enum` whose enumerators are initialised from the mangled aliases.
// clang wraps each initialiser in ImplicitCastExpr (because the inner
// `enum` has no fixed underlying type), so the parser must look one
// level deeper than ConstantExpr to recover the integer value. This
// reproduces the `Anchor::Start = Anchor::End = 1` collapse that the
// fix in findEnumConstantValue addresses.
class AnchorEnums {
public:
    enum AnchorRaw {
        ANCHOR_UNSPECIFIED = 0,
        START = 1,
        END = 2,
    };
};

class Anchor {
public:
    enum Value {
        UNSPECIFIED = AnchorEnums::ANCHOR_UNSPECIFIED,
        START = AnchorEnums::START,
        END = AnchorEnums::END,
    };
};

// Implicit enum exercises the auto-increment fallback: clang emits no
// ConstantExpr for `A`, `B`, `C` because there is no `= N` initialiser,
// so the parser must compute prev+1 to recover 0, 1, 2.
enum ImplicitColor {
    IMPLICIT_RED,
    IMPLICIT_GREEN,
    IMPLICIT_BLUE,
};

// POD struct for computation results
struct Result {
    double value;
    bool ok;
    std::string error_message;
};

// Calculator class with methods and inheritance
class Calculator {
public:
    Calculator();
    virtual ~Calculator();

    // Basic operations
    double compute(Operation op, double a, double b) const;
    Result compute_safe(Operation op, double a, double b) const;

    // History
    void add_to_history(double value);
    std::vector<double> get_history() const;
    void clear_history();

    // Name
    const std::string& name() const;
    void set_name(const std::string& name);

private:
    std::vector<double> history_;
    std::string name_;
};

// Derived class
class ScientificCalculator : public Calculator {
public:
    double power(double base, double exp) const;
    double sqrt(double value) const;
};

// StatefulCounter: a class with MIXED public and private state used to
// regression-test handle-based state preservation across the bridge.
//
// - `label` is a public field (visible via the direct accessor below)
// - `sum_` and `call_count_` are private state that can only be observed
//   through the public methods `total()` and `call_count()`.
// - No virtual methods, so the existing handle-promotion rules based on
//   virtual dispatch do NOT apply; this class exercises the code path
//   that reclassifies mixed-field classes via HasPrivateFields.
//
// If the bridge treated this as a value type, each `add()` call would run
// against a freshly-constructed temporary and `sum_` would reset between
// calls. The test `TestStatefulCounterStatePreservation` verifies that
// does not happen.
class StatefulCounter {
public:
    StatefulCounter();
    explicit StatefulCounter(int initial_id);

    int id() const;                 // reads public field `label`
    void set_label(int new_label);  // writes public field `label`

    void add(int delta);            // mutates private sum_ and call_count_
    int total() const;              // reads private sum_
    int call_count() const;         // reads private call_count_
    void reset();

    int label;  // public field: intentionally exposed
private:
    int sum_;
    int call_count_;
};

// Abstract interface that users can implement from Go. The bridge emits a
// trampoline that dispatches each pure-virtual through the callback wire
// (wasmify_callback_invoke) so a Go type can satisfy the C++ vtable.
class Logger {
public:
    virtual ~Logger() = default;
    virtual void log(const std::string& message) = 0;
    virtual int level() const = 0;
};

// ManagedValue exercises the "concrete class with non-public destructor"
// case that googlesql's type hierarchy uses. Lifetime is owned by
// ManagedFactory (analogous to TypeFactory); user code only ever sees
// raw pointers and must not delete them. The bridge must still emit a
// Service for ManagedValue with all its accessors, but must NOT emit a
// Free RPC (the caller can't safely delete).
class ManagedFactory;

class ManagedValue {
public:
    // Accessors are public.
    int kind() const;                 // returns an int label
    const std::string& tag() const;   // returns the associated string

private:
    // Non-public destructor — only ManagedFactory can delete.
    ~ManagedValue();
    ManagedValue(int kind, const std::string& tag);

    int kind_;
    std::string tag_;

    friend class ManagedFactory;
};

class ManagedFactory {
public:
    ManagedFactory();
    ~ManagedFactory();

    // Creates and owns a new ManagedValue. Caller must not delete.
    const ManagedValue* make(int kind, const std::string& tag);

private:
    std::vector<ManagedValue*> owned_;
};

// ----------------------------------------------------------------------
// Inheritance fixtures
//
// Three patterns live below so the simplelib_proto tests can exercise
// every way the generator renders C++ inheritance into Go:
//
//   (A) Abstract base + concrete subclasses returned by handle →
//       exercises type assertion as the downcast mechanism. The
//       generator MUST NOT emit ToXxx() RPCs (forbidden by CLAUDE.md).
//
//   (B) Multi-level inheritance (Animal → Mammal → Canine → Dog) so we
//       can prove every accessor along a 4-level chain is reachable on
//       the leaf struct via Go method promotion.
//
//   (C) Multiple inheritance (Product : public Named, public Priced) so
//       the generator is forced to embed both parents and every method
//       from either parent remains callable on the derived struct.
// ----------------------------------------------------------------------

// (A) Abstract base for downcast-via-assertion test.
class Shape {
public:
    virtual ~Shape() = default;
    virtual double area() const = 0;
};

class Circle : public Shape {
public:
    explicit Circle(double r);
    double area() const override;
    double radius() const;

private:
    double r_;
};

class Square : public Shape {
public:
    explicit Square(double s);
    double area() const override;
    double side() const;

private:
    double s_;
};

class ShapeBox {
public:
    ShapeBox();
    ~ShapeBox();
    void add(Shape* s);
    Shape* get(int i) const;  // abstract return → exercises downcast flow
    int size() const;

private:
    std::vector<Shape*> shapes_;
};

// (B) Multi-level chain. Every accessor defined at each level must be
// callable directly on the leaf Dog handle.
class Animal {
public:
    virtual ~Animal() = default;
    const std::string& species() const;
    void set_species(const std::string& s);

protected:
    std::string species_;
};

class Mammal : public Animal {
public:
    int legs() const;
    void set_legs(int n);

protected:
    int legs_ = 4;
};

class Canine : public Mammal {
public:
    const std::string& breed() const;
    void set_breed(const std::string& b);

protected:
    std::string breed_;
};

class Dog : public Canine {
public:
    Dog();
    int bark_volume() const;
    void set_bark_volume(int v);

private:
    int bark_volume_ = 50;
};

// (C) Multiple inheritance. Product picks up Named and Priced accessors
// side by side. Both sides of the diamond are non-virtual; the Go struct
// must embed both parents so method promotion reaches either tree.
class Named {
public:
    const std::string& named_name() const;
    void set_named_name(const std::string& n);

protected:
    std::string named_name_;
};

class Priced {
public:
    double priced_price() const;
    void set_priced_price(double p);

protected:
    double priced_price_ = 0;
};

class Product : public Named, public Priced {
public:
    Product();
    int stock() const;
    void set_stock(int s);

private:
    int stock_ = 0;
};

// Namespace-scope typedef whose underlying spelling references `Result`
// without a namespace qualifier ("std::vector<Result>"). Regression test
// for inner-identifier qualification: postProcessTypedefAliases must
// rewrite this to `std::vector<calc::Result>` so the generated proto /
// bridge see the FQ name. If the aliased function below appears in
// api-spec.json with `Result` instead of `calc::Result`, the rewrite is
// missing.
typedef std::vector<Result> ResultList;

ResultList aggregate_results(const std::vector<double>& values);

// TextNode / HeadingNode regression-test the abstract-handle naming
// rule. TextNode itself ends in "Node", so the generator must emit the
// Go interface as `TextNode` (not the doubled `TextNodeNode`) and
// rename the abstract struct to `TextNodeBase` so it does not collide
// with the interface. HeadingNode is a concrete descendant whose struct
// embeds `*TextNodeBase`.
class TextNode {
public:
    virtual ~TextNode() = default;
    virtual std::string render() const = 0;
};

class HeadingNode : public TextNode {
public:
    HeadingNode(int level, const std::string& text);
    std::string render() const override;

private:
    int level_;
    std::string text_;
};

// Free functions
double add(double a, double b);
std::string format_result(const Result& r);
int version();

// Exercises the Logger callback end-to-end: invokes both virtuals on the
// supplied logger and returns the level it reported.
int run_with_logger(Logger* logger, const std::string& message);

} // namespace calc
