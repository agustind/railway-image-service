# syntax=docker/dockerfile:1
ARG VERSION=1.23.1
ARG BUILDPLATFORM
ARG BUILDER=docker.io/library/golang

FROM --platform=${BUILDPLATFORM} ${BUILDER}:${VERSION} AS base
SHELL ["/bin/bash", "-o", "pipefail", "-c"]

FROM base AS deps
WORKDIR /go/src/app
COPY go.mod* go.sum* ./
RUN go mod download && go mod tidy

FROM deps AS vips-builder
ARG VIPS_VERSION=8.16.0
ENV PKG_CONFIG_PATH=/usr/local/lib/pkgconfig

RUN DEBIAN_FRONTEND=noninteractive \
    apt-get update && \
    apt-get install --no-install-recommends -y \
    ca-certificates automake build-essential curl \
    meson ninja-build pkg-config \
    gobject-introspection gtk-doc-tools libglib2.0-dev \
    libjpeg62-turbo-dev libpng-dev libwebp-dev libtiff-dev \
    libexif-dev libxml2-dev libpoppler-glib-dev swig \
    libpango1.0-dev libmatio-dev libopenslide-dev libcfitsio-dev \
    libopenjp2-7-dev liblcms2-dev libgsf-1-dev libfftw3-dev \
    liborc-0.4-dev librsvg2-dev libimagequant-dev libaom-dev \
    libheif-dev libspng-dev libcgif-dev && \
    apt-get clean && \
    rm -rf /var/lib/apt/lists/*

RUN cd /tmp && \
    curl -fsSLO https://github.com/libvips/libvips/releases/download/v${VIPS_VERSION}/vips-${VIPS_VERSION}.tar.xz && \
    tar xf vips-${VIPS_VERSION}.tar.xz && \
    cd vips-${VIPS_VERSION} && \
    meson setup _build \
    --buildtype=release \
    --strip \
    --prefix=/usr/local \
    --libdir=lib \
    --optimization=3 \
    -Dgtk_doc=false \
    -Dmagick=disabled \
    -Dintrospection=disabled && \
    ninja -C _build && \
    ninja -C _build install && \
    ldconfig && \
    rm -rf /usr/local/lib/libvips-cpp.* \
           /usr/local/lib/*.a \
           /usr/local/lib/*.la

FROM vips-builder AS build
WORKDIR /go/src/app
ARG TARGETOS
ARG TARGETARCH

COPY . .
RUN GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build -trimpath -ldflags="-s -w" -o /go/bin/app ./cmd/server

# Use imagor base image which already has all vips dependencies
FROM ghcr.io/cshum/imagor:latest
WORKDIR /app

LABEL org.opencontainers.image.source="https://github.com/jaredLunde/railway-image-service" \
      org.opencontainers.image.description="Image processing service with libvips" \
      maintainer="jared.lunde@gmail.com"

# Create required directories
RUN mkdir -p /app/data/uploads /app/data/db

# Copy your compiled app
COPY --from=build --chown=nobody:nogroup /go/bin/app ./app
RUN chmod +x ./app

ENV UPLOAD_PATH=/app/data/uploads \
    LEVELDB_PATH=/app/data/db \
    PORT=8080 \
    VIPS_WARNING=0 \
    GOGC=100 \
    GOMAXPROCS=4

USER nobody

EXPOSE ${PORT}
ENTRYPOINT ["./app"]