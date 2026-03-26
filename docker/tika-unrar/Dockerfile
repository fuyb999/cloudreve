FROM maven:3.9.9-eclipse-temurin-21 AS custom-parser-builder

WORKDIR /build

COPY docker/tika-unrar/charset-patch/pom.xml /build/pom.xml
COPY docker/tika-unrar/charset-patch/src /build/src

RUN mvn -q package

FROM apache/tika:3.2.3.0-full

USER root

RUN apt-get update \
    && apt-get install -y --no-install-recommends \
        fontconfig \
        fonts-noto-cjk \
        locales \
        unrar-free \
    && sed -i 's/^# *zh_CN.UTF-8 UTF-8/zh_CN.UTF-8 UTF-8/' /etc/locale.gen \
    && locale-gen zh_CN.UTF-8 \
    && update-locale LANG=zh_CN.UTF-8 LC_ALL=zh_CN.UTF-8 LANGUAGE=zh_CN:zh \
    && fc-cache -f \
    && ln -sf /usr/bin/unrar-free /usr/local/bin/unrar \
    && mkdir -p /tika-extras \
    && rm -rf /var/lib/apt/lists/*

COPY --from=custom-parser-builder /build/target/tika-archive-charset-patch-1.0.0.jar /tika-extras/tika-archive-charset-patch.jar
COPY docker/tika-unrar/tika-config.xml /tika-config.xml

ENV LANG=zh_CN.UTF-8
ENV LC_ALL=zh_CN.UTF-8
ENV LANGUAGE=zh_CN:zh
ENV TIKA_CONFIG=file:///tika-config.xml

USER 35002:35002
