# Internal base of the sandbox images (not selectable with -image): opencode
# plus git, running as a non-root user. oc starts it with --user <host
# uid>:<host gid> so files written to the mounted working directory keep the
# host user's ownership; HOME is therefore world-writable rather than owned by
# a specific uid.
FROM node:22-slim

RUN apt-get update \
 && apt-get install -y --no-install-recommends git ca-certificates \
 && rm -rf /var/lib/apt/lists/* \
 && npm install -g opencode-ai \
 && npm cache clean --force

RUN useradd --create-home --home-dir /home/oc --shell /bin/bash oc \
 && chmod 0777 /home/oc

ENV HOME=/home/oc
USER oc
WORKDIR /workspace
ENTRYPOINT ["opencode"]
