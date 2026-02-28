# Stage 1:
# Use base Alpine image to prepare our binary
FROM golang:1.26 as build-stage
LABEL app="otelstor-build-go1-26"
WORKDIR /app

# Copy all the files from the base of our repository to the current directory defined above
COPY . .

# Compile the application to a single statically-linked binary file
RUN CGO_ENABLED=0 go build -ldflags '-extldflags "-static"' -tags timetzdata -o otelstor   ./main.go
RUN CGO_ENABLED=0 go build -ldflags '-extldflags "-static"' -tags timetzdata -o testclient cmd/testclient/main.go
RUN CGO_ENABLED=0 go build -ldflags '-extldflags "-static"' -tags timetzdata -o oteldash   cmd/dashboard/main.go
RUN CGO_ENABLED=0 go build -ldflags '-extldflags "-static"' -tags timetzdata

# Stage 2:

FROM gcr.io/distroless/static-debian11 AS release-stage
WORKDIR /
COPY --from=build-stage /etc/passwd      /etc/passwd

COPY --from=build-stage /app/golauncher  /golauncher
COPY --from=build-stage /app/golauncher.cfg  /golauncher.cfg

COPY --from=build-stage /app/otelstor    /otelstor
COPY --from=build-stage /app/testclient  /testclient
COPY --from=build-stage /app/oteldash    /oteldash

EXPOSE 4137/tcp
EXPOSE 10731/tcp

# Run our app by directly executing the binary
ENTRYPOINT ["./golauncher"]

VOLUME /data
LABEL app="otelstor"
