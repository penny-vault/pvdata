FROM --platform=$BUILDPLATFORM node:alpine AS ui-builder
WORKDIR /src
COPY web/ui/package.json web/ui/package-lock.json ./
RUN npm ci
COPY web/ui/ ./
RUN npm run build

FROM golang AS builder
WORKDIR /go/src
RUN apt-get update && apt-get install -y --no-install-recommends make && rm -rf /var/lib/apt/lists/*
COPY ./ .
COPY --from=ui-builder /src/dist ./web/ui/dist
RUN make build

FROM pennyvault/playwright-go
COPY --from=builder /go/src/pvdata /home/ubuntu
ENTRYPOINT ["/home/ubuntu/pvdata"]
CMD ["serve"]
