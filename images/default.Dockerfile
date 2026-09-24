# The default sandbox image: the base (opencode, git) plus a few general
# command-line tools for the agent.
#
# A layer on base: oc builds base first and passes its tag in as OC_BASE (the
# value here is only a placeholder that keeps the FROM lint quiet).
ARG OC_BASE=oc-sandbox-base
FROM ${OC_BASE}

USER root
RUN apt-get update \
 && apt-get install -y --no-install-recommends jq ripgrep curl \
 && rm -rf /var/lib/apt/lists/*
USER oc
