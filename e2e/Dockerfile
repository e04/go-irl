# Toolchain for the E2E suite: Go plus srtla_send built from irlserver/srtla.
# srtla_send is Linux-only (SOCK_NONBLOCK, endian.h), so non-Linux hosts run
# the suite in this image via e2e/run-docker.sh.
FROM golang:1.24-bookworm

# Keep in sync with SRTLA_REF in .github/workflows/test.yml.
ARG SRTLA_REF=b8359bc80ce99ac4fc8f603cfa455cdd0a0a4241

RUN apt-get update \
 && apt-get install -y --no-install-recommends cmake g++ git \
 && rm -rf /var/lib/apt/lists/*

# Debug build: sender.cpp reads the clock inside assert() (e.g.
# `assert(get_ms(&ms) == 0)`), so NDEBUG builds never register a link.
RUN git clone https://github.com/irlserver/srtla.git /src/srtla \
 && cd /src/srtla \
 && git checkout -q "$SRTLA_REF" \
 && git submodule update --init --depth 1 \
 && cmake -S . -B build -DCMAKE_BUILD_TYPE=Debug \
 && cmake --build build --target srtla_send -j"$(nproc)" \
 && install build/srtla_send /usr/local/bin/srtla_send \
 && rm -rf /src/srtla

ENV SRTLA_SEND=/usr/local/bin/srtla_send
WORKDIR /work
