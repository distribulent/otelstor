#!/bin/bash

PKG="otelstor"
REPO="localhost"
IMAGE=${REPO}/${PKG}

VERSION=$(git rev-parse --short HEAD)

podman build . --tag ${IMAGE}:${VERSION}
podman tag ${IMAGE}:${VERSION} ${PKG}:latest
podman tag ${IMAGE}:${VERSION} ${IMAGE}:latest
