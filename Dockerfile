FROM node:alpine AS ui-builder
WORKDIR /src
COPY web/ui/package.json web/ui/package-lock.json ./
RUN npm ci
COPY web/ui/ ./
RUN npm run build

FROM golang:alpine AS builder
WORKDIR /go/src
RUN apk add git make
COPY ./ .
COPY --from=ui-builder /src/dist ./web/ui/dist
RUN make build

FROM alpine

# Add pv as a user
RUN apk add tzdata && adduser -D pv
# Run pv as non-privileged
USER pv
WORKDIR /home/pv

COPY --from=builder /go/src/pvdata /home/pv
ENTRYPOINT ["/home/pv/pvdata"]
CMD ["serve"]
