FROM golang:1.26-bookworm AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -o /out/quiz ./cmd/quiz

FROM build AS test
RUN apt-get update && apt-get install -y --no-install-recommends ffmpeg \
    && rm -rf /var/lib/apt/lists/*
CMD ["go", "test", "-race", "./..."]

FROM debian:bookworm-slim
RUN apt-get update && apt-get install -y --no-install-recommends \
    ca-certificates ffmpeg python3 python3-venv \
    && python3 -m venv /opt/tools \
    && /opt/tools/bin/pip install --no-cache-dir 'yt-dlp[default]' 'ytmusicapi==1.12.3' \
    && rm -rf /var/lib/apt/lists/* \
    && useradd --uid 10001 --create-home quiz \
    && mkdir -p /app/data && chown quiz:quiz /app/data
ENV PATH="/opt/tools/bin:${PATH}" HTTP_ADDR="0.0.0.0:8080"
WORKDIR /app
COPY --from=build /out/quiz /app/quiz
COPY --from=node:22-bookworm-slim /usr/local/bin/node /usr/local/bin/node
USER quiz
EXPOSE 8080
ENTRYPOINT ["/app/quiz"]
