FROM golang:1.25-alpine AS build

WORKDIR /src
COPY go.mod go.sum ./
COPY *.go session.html ./
COPY public ./public
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/helix-orchestrator .

FROM alpine:3.22
RUN adduser -D -u 1000 app
WORKDIR /app
COPY --from=build /out/helix-orchestrator /usr/local/bin/helix-orchestrator
USER app
ENV PORT=8080
EXPOSE 8080
ENTRYPOINT ["helix-orchestrator"]
