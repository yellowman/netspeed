# netspeed - C speed test client
# Targets: Linux, FreeBSD, OpenBSD, NetBSD

CC ?= cc
CFLAGS = -Wall -Wextra -Werror -O2 -std=c11 -D_POSIX_C_SOURCE=200809L
LDFLAGS = -lssl -lcrypto -lm

# Platform detection
UNAME := $(shell uname -s)
ifeq ($(UNAME),Linux)
    CFLAGS += -D_GNU_SOURCE
    LDFLAGS += -lpthread
endif
ifeq ($(UNAME),FreeBSD)
    CFLAGS += -I/usr/local/include
    LDFLAGS += -L/usr/local/lib -lpthread
endif
ifeq ($(UNAME),OpenBSD)
    CFLAGS += -I/usr/local/include
    LDFLAGS += -L/usr/local/lib -lpthread
endif
ifeq ($(UNAME),NetBSD)
    CFLAGS += -I/usr/pkg/include
    LDFLAGS += -L/usr/pkg/lib -lpthread
endif

# Debug build
ifdef DEBUG
    CFLAGS += -g -DDEBUG -O0
endif

SRCDIR = src
INCDIR = include
OBJDIR = obj

SRCS = $(wildcard $(SRCDIR)/*.c)
OBJS = $(patsubst $(SRCDIR)/%.c,$(OBJDIR)/%.o,$(SRCS))
TARGET = netspeed

.PHONY: all clean install

all: $(TARGET)

$(TARGET): $(OBJS)
	$(CC) $(OBJS) -o $@ $(LDFLAGS)

$(OBJDIR)/%.o: $(SRCDIR)/%.c | $(OBJDIR)
	$(CC) $(CFLAGS) -I$(INCDIR) -c $< -o $@

$(OBJDIR):
	mkdir -p $(OBJDIR)

clean:
	rm -rf $(OBJDIR) $(TARGET)

install: $(TARGET)
	install -m 755 $(TARGET) /usr/local/bin/

# Development helpers
format:
	clang-format -i $(SRCDIR)/*.c $(INCDIR)/*.h

check:
	cppcheck --enable=all -I$(INCDIR) $(SRCDIR)/*.c
