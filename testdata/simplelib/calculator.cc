#include "calculator.h"
#include <cmath>
#include <sstream>

namespace calc {

Calculator::Calculator() : name_("default") {}
Calculator::~Calculator() {}

double Calculator::compute(Operation op, double a, double b) const {
    switch (op) {
    case Operation::Add: return a + b;
    case Operation::Subtract: return a - b;
    case Operation::Multiply: return a * b;
    case Operation::Divide: return b != 0 ? a / b : 0;
    }
    return 0;
}

Result Calculator::compute_safe(Operation op, double a, double b) const {
    if (op == Operation::Divide && b == 0) {
        return Result{0, false, "division by zero"};
    }
    return Result{compute(op, a, b), true, ""};
}

void Calculator::add_to_history(double value) {
    history_.push_back(value);
}

std::vector<double> Calculator::get_history() const {
    return history_;
}

void Calculator::clear_history() {
    history_.clear();
}

const std::string& Calculator::name() const {
    return name_;
}

void Calculator::set_name(const std::string& name) {
    name_ = name;
}

double ScientificCalculator::power(double base, double exp) const {
    return std::pow(base, exp);
}

double ScientificCalculator::sqrt(double value) const {
    return std::sqrt(value);
}

StatefulCounter::StatefulCounter() : label(0), sum_(0), call_count_(0) {}

StatefulCounter::StatefulCounter(int initial_id)
    : label(initial_id), sum_(0), call_count_(0) {}

int StatefulCounter::id() const { return label; }
void StatefulCounter::set_label(int new_label) { label = new_label; }

void StatefulCounter::add(int delta) {
    sum_ += delta;
    ++call_count_;
}

int StatefulCounter::total() const { return sum_; }
int StatefulCounter::call_count() const { return call_count_; }

void StatefulCounter::reset() {
    sum_ = 0;
    call_count_ = 0;
}

ManagedValue::ManagedValue(int kind, const std::string& tag)
    : kind_(kind), tag_(tag) {}
ManagedValue::~ManagedValue() = default;

int ManagedValue::kind() const { return kind_; }
const std::string& ManagedValue::tag() const { return tag_; }

ManagedFactory::ManagedFactory() = default;
ManagedFactory::~ManagedFactory() {
    for (auto* v : owned_) {
        delete v;
    }
}

const ManagedValue* ManagedFactory::make(int kind, const std::string& tag) {
    auto* v = new ManagedValue(kind, tag);
    owned_.push_back(v);
    return v;
}

// ---- (A) Shape hierarchy ---------------------------------------------

Circle::Circle(double r) : r_(r) {}
double Circle::area() const { return 3.141592653589793 * r_ * r_; }
double Circle::radius() const { return r_; }

Square::Square(double s) : s_(s) {}
double Square::area() const { return s_ * s_; }
double Square::side() const { return s_; }

ShapeBox::ShapeBox() = default;
ShapeBox::~ShapeBox() {
    for (auto* s : shapes_) delete s;
}
void ShapeBox::add(Shape* s) { shapes_.push_back(s); }
Shape* ShapeBox::get(int i) const { return shapes_.at(i); }
int ShapeBox::size() const { return static_cast<int>(shapes_.size()); }

// ---- (B) Animal → Mammal → Canine → Dog ------------------------------

const std::string& Animal::species() const { return species_; }
void Animal::set_species(const std::string& s) { species_ = s; }

int Mammal::legs() const { return legs_; }
void Mammal::set_legs(int n) { legs_ = n; }

const std::string& Canine::breed() const { return breed_; }
void Canine::set_breed(const std::string& b) { breed_ = b; }

Dog::Dog() {
    species_ = "dog";
    legs_ = 4;
}
int Dog::bark_volume() const { return bark_volume_; }
void Dog::set_bark_volume(int v) { bark_volume_ = v; }

// ---- (C) Multiple inheritance: Product : public Named, public Priced -

const std::string& Named::named_name() const { return named_name_; }
void Named::set_named_name(const std::string& n) { named_name_ = n; }

double Priced::priced_price() const { return priced_price_; }
void Priced::set_priced_price(double p) { priced_price_ = p; }

Product::Product() = default;
int Product::stock() const { return stock_; }
void Product::set_stock(int s) { stock_ = s; }

double add(double a, double b) {
    return a + b;
}

ResultList aggregate_results(const std::vector<double>& values) {
    ResultList out;
    out.reserve(values.size());
    for (double v : values) {
        out.push_back(Result{v, true, std::string{}});
    }
    return out;
}

HeadingNode::HeadingNode(int level, const std::string& text)
    : level_(level), text_(text) {}

std::string HeadingNode::render() const {
    return std::string(level_, '#') + " " + text_;
}

std::string format_result(const Result& r) {
    std::ostringstream oss;
    if (r.ok) {
        oss << "Result: " << r.value;
    } else {
        oss << "Error: " << r.error_message;
    }
    return oss.str();
}

int version() {
    return 1;
}

int run_with_logger(Logger* logger, const std::string& message) {
    if (!logger) return -1;
    logger->log(message);
    return logger->level();
}

} // namespace calc
