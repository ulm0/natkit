# test/init.sh's image: natkit's, plus what the tests themselves use (ip link add, the mock's call log through curl
# and yq). The image natkit ships has none of these.
ARG IMG
FROM ${IMG}
RUN apk add --no-cache curl yq-go iproute2
