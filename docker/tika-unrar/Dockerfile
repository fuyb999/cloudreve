FROM apache/tika:3.2.3.0-full

USER root

RUN apt-get update \
    && apt-get install -y --no-install-recommends unrar-free \
    && ln -sf /usr/bin/unrar-free /usr/local/bin/unrar \
    && rm -rf /var/lib/apt/lists/*

COPY docker/tika-unrar/tika-config.xml /tika-config.xml

ENV TIKA_CONFIG=file:///tika-config.xml

USER 35002:35002
