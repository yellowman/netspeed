# netspeed - legacy C speed-test client
# Targets: Linux, FreeBSD, OpenBSD, NetBSD
#
# This client is retained as an unsupported compatibility implementation. The
# supported protocol-v2 client is cmd/netspeed. See README.md in this directory.

CC ?= cc
CPPFLAGS ?=
CFLAGS ?= -O2
WARNFLAGS ?= -Wall -Wextra -Werror
CSTD ?= -std=c11
LDFLAGS ?=
LDLIBS ?= -lssl -lcrypto -lm

# Keep required feature-test macros and warning policy even when callers supply
# CFLAGS on the command line (as CI and packaging systems commonly do).
override CFLAGS += $(WARNFLAGS) $(CSTD)

UNAME := $(shell uname -s)
ifeq ($(UNAME),Linux)
    override CPPFLAGS += -D_POSIX_C_SOURCE=200809L -D_GNU_SOURCE
    override LDLIBS += -lpthread
endif
ifeq ($(UNAME),FreeBSD)
    override CPPFLAGS += -I/usr/local/include
    override LDFLAGS += -L/usr/local/lib
    override LDLIBS += -lpthread
endif
ifeq ($(UNAME),OpenBSD)
    # LibreSSL is provided by the base system. Do not define
    # _POSIX_C_SOURCE: it hides interfaces used by this legacy client.
    override LDLIBS += -lpthread
endif
ifeq ($(UNAME),NetBSD)
    override CPPFLAGS += -I/usr/pkg/include
    override LDFLAGS += -L/usr/pkg/lib
    override LDLIBS += -lpthread
endif

ifdef DEBUG
    override CFLAGS += -g -O0 -DDEBUG
endif

SRCDIR := src
INCDIR := include
OBJDIR := obj
TARGET := netspeed

SRCS := $(wildcard $(SRCDIR)/*.c)
OBJS := $(patsubst $(SRCDIR)/%.c,$(OBJDIR)/%.o,$(SRCS))

.PHONY: all clean install format check check-build check-compilers

all: $(TARGET)

$(TARGET): $(OBJS)
	$(CC) $(LDFLAGS) $(OBJS) $(LDLIBS) -o $@

$(OBJDIR)/%.o: $(SRCDIR)/%.c | $(OBJDIR)
	$(CC) $(CPPFLAGS) $(CFLAGS) -I$(INCDIR) -c $< -o $@

$(OBJDIR):
	mkdir -p $(OBJDIR)

clean:
	rm -rf $(OBJDIR) $(TARGET)

install: $(TARGET)
	install -m 755 $(TARGET) /usr/local/bin/

format:
	clang-format -i $(SRCDIR)/*.c $(INCDIR)/*.h

check:
	cppcheck --enable=all -I$(INCDIR) $(SRCDIR)/*.c

check-build: clean all
	./$(TARGET) --version

# Compile the unsupported compatibility client with both toolchains. This is a
# source-health check, not protocol-v2 release qualification.
check-compilers:
	$(MAKE) clean
	$(MAKE) CC=gcc all
	./$(TARGET) --version
	$(MAKE) clean
	$(MAKE) CC=clang all
	./$(TARGET) --version
	$(MAKE) clean
