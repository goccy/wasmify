CXX ?= clang++
CXXFLAGS ?= -std=c++17 -Wall -O2
AR ?= ar

all: libcalculator.a

calculator.o: calculator.cc calculator.h
	$(CXX) $(CXXFLAGS) -c calculator.cc -o calculator.o

libcalculator.a: calculator.o
	$(AR) rcs libcalculator.a calculator.o

clean:
	rm -f *.o *.a

.PHONY: all clean
