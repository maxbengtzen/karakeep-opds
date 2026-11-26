#!/bin/sh
# Records golden files from the Python original. Re-run only when fixtures change on purpose.
set -eu
cd "$(dirname "$0")/../.."
IMAGE=${IMAGE:-ghcr.io/yazdipour/karakeep-opds:0.0.3}
rm -rf testdata/golden && mkdir -p testdata/golden
docker run --rm -u "$(id -u):$(id -g)" \
	-e KARAKEEP_BASE_URL=http://karakeep.test -e KARAKEEP_API_TOKEN=test-token \
	-e OPDS_USERNAME=reader -e OPDS_PASSWORD=secret \
	-v "$PWD/testdata/fixtures:/fix:ro" -v "$PWD/testdata/golden:/out" \
	-v "$PWD/scripts/golden/record.py:/rec/record.py:ro" \
	--entrypoint python "$IMAGE" -I /rec/record.py /fix /out
