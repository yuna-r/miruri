CC ?= cc
AR ?= ar
RANLIB ?= ranlib
CFLAGS ?= -O2 -Wall -Wextra -pedantic

all: miruri-hello libgreeting.a

miruri-hello: src/main.o libgreeting.a
	$(CC) $(CFLAGS) -o $@ src/main.o libgreeting.a

libgreeting.a: src/greeting.o
	$(AR) rcs $@ src/greeting.o
	$(RANLIB) $@

src/main.o: src/main.c src/greeting.h
	$(CC) $(CFLAGS) -c -o $@ src/main.c

src/greeting.o: src/greeting.c src/greeting.h
	$(CC) $(CFLAGS) -c -o $@ src/greeting.c

clean:
	rm -f src/*.o miruri-hello libgreeting.a

.PHONY: all clean
