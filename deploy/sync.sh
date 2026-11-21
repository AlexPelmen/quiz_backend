#!/usr/bin/env bash
set -euo pipefail

if [[ $# != 1 ]]; then
    echo "Expected the service name." >&2
    exit 2
fi

service=$1
case "$service" in
    api)
        repo=quiz_backend
        revision_var=API_REVISION
        check_url=http://127.0.0.1:8080/ready
        ;;
    frontend)
        repo=quiz_frontend
        revision_var=FRONTEND_REVISION
        check_url=http://127.0.0.1:3000/
        ;;
    *)
        echo "Unknown service: $service" >&2
        exit 2
        ;;
esac

umask 077
cd /srv/music-quiz
exec 9>deploy.lock
flock -w 600 9

source_url="https://github.com/AlexPelmen/$repo.git"
master_ref=refs/heads/master
master_revision() {
    git ls-remote --exit-code "$source_url" "$master_ref" | cut -f1
}

revision=$(master_revision)
if [[ ! "$revision" =~ ^[0-9a-f]{40}$ ]]; then
    echo "Could not identify master for $repo." >&2
    exit 1
fi

previous=$(sed -n "s/^$revision_var=//p" images.env)
if [[ "$previous" == "$revision" ]]; then
    exit 0
fi

ci_result=$(curl --fail --silent --show-error --max-time 20 --get \
    --data-urlencode "head_sha=$revision" --data 'branch=master' \
    --data 'event=push' --data 'status=success' --data 'per_page=1' \
    "https://api.github.com/repos/AlexPelmen/$repo/actions/workflows/ci.yml/runs" |
    python3 -c 'import json, sys; print(json.load(sys.stdin)["total_count"])')
if [[ "$ci_result" == 0 ]]; then
    echo "Waiting for a successful CI run for $repo@$revision."
    exit 0
fi

build_dir=$(mktemp -d)
candidate=$(mktemp images.env.XXXXXX)
trap 'rm -rf -- "$build_dir"; rm -f -- "$candidate"' EXIT
git -C "$build_dir" init --quiet --bare
git -C "$build_dir" fetch --quiet --depth=1 "$source_url" "$master_ref"
if [[ "$(git -C "$build_dir" rev-parse FETCH_HEAD)" != "$revision" ]]; then
    echo "Master moved while fetching $repo. The next sync will check the new revision."
    exit 0
fi

image="music-quiz-$service:$revision"
git -C "$build_dir" archive "$revision" | docker build --tag "$image" -
latest=$(master_revision)
if [[ "$latest" != "$revision" ]]; then
    docker image rm "$image"
    echo "Master moved while building $repo. The next sync will check the new revision."
    exit 0
fi

compose() {
    local images_file=$1
    shift
    docker compose --env-file app.env --env-file "$images_file" "$@"
}

wait_for_release() {
    local images_file=$1
    local attempt
    for ((attempt = 1; attempt <= 45; attempt++)); do
        if curl --fail --silent --max-time 3 "$check_url" >/dev/null &&
            { grep -q '=unpublished$' "$images_file" ||
                curl --fail --silent --max-time 3 http://127.0.0.1:3000/ready >/dev/null; }; then
            return
        fi
        sleep 2
    done
    echo "Readiness check failed for $service." >&2
    return 1
}

sed "s/^$revision_var=.*/$revision_var=$revision/" images.env > "$candidate"

if compose "$candidate" up --detach --no-deps --wait "$service" &&
    wait_for_release "$candidate"; then
    mv -- "$candidate" images.env
    echo "Deployed $image."
    if [[ "$previous" != unpublished ]]; then
        if ! docker image rm "music-quiz-$service:$previous"; then
            echo "Could not remove the previous image for $service." >&2
        fi
    fi
    exit 0
fi

compose "$candidate" ps "$service"
if [[ "$previous" == unpublished ]]; then
    compose "$candidate" rm --stop --force "$service"
else
    echo "Restoring $service to $previous." >&2
    compose images.env up --detach --no-deps --wait "$service"
    wait_for_release images.env
fi
docker image rm "$image"
exit 1
