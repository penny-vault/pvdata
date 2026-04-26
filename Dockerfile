FROM golang:alpine AS builder
WORKDIR /go/src
RUN apk add git make
COPY ./ .
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