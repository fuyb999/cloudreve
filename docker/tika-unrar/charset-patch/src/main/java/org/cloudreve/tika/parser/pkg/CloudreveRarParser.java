/*
 * Licensed to the Apache Software Foundation (ASF) under one or more
 * contributor license agreements.  See the NOTICE file distributed with
 * this work for additional information regarding copyright ownership.
 * The ASF licenses this file to You under the Apache License, Version 2.0
 * (the "License"); you may not use this file except in compliance with
 * the License.  You may obtain a copy of the License at
 *
 *     http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */
package org.cloudreve.tika.parser.pkg;

import java.io.ByteArrayOutputStream;
import java.io.IOException;
import java.io.InputStream;
import java.nio.charset.Charset;
import java.nio.charset.StandardCharsets;
import java.nio.file.Files;
import java.nio.file.Path;
import java.nio.file.attribute.BasicFileAttributes;
import java.util.Comparator;
import java.util.Date;
import java.util.List;
import java.util.Set;
import java.util.stream.Stream;

import com.github.junrar.Archive;
import com.github.junrar.exception.RarException;
import com.github.junrar.exception.UnsupportedRarV5Exception;
import com.github.junrar.rarfile.FileHeader;
import org.xml.sax.ContentHandler;
import org.xml.sax.SAXException;

import org.apache.tika.config.Field;
import org.apache.tika.exception.EncryptedDocumentException;
import org.apache.tika.exception.TikaException;
import org.apache.tika.extractor.EmbeddedDocumentExtractor;
import org.apache.tika.extractor.EmbeddedDocumentUtil;
import org.apache.tika.io.TemporaryResources;
import org.apache.tika.io.TikaInputStream;
import org.apache.tika.metadata.Metadata;
import org.apache.tika.mime.MediaType;
import org.apache.tika.parser.ParseContext;
import org.apache.tika.parser.Parser;
import org.apache.tika.sax.XHTMLContentHandler;

/**
 * Rar parser with legacy Chinese filename decoding for non-unicode entries.
 * Rar5 archives are extracted with unrar-free to avoid Java path encoding issues.
 */
public class CloudreveRarParser implements Parser {
    private static final long serialVersionUID = 6157727985054451501L;
    private static final String UNRAR_BINARY = "unrar-free";
    private static final String UTF8_FALLBACK_LOCALE = "C.UTF-8";

    private static final Set<MediaType> SUPPORTED_TYPES =
            Set.of(MediaType.application("x-rar-compressed"), MediaType.application("vnd.rar"));

    private Charset legacyCharset =
            ArchiveFilenameEncodingUtil.toCharset(ArchiveFilenameEncodingUtil.DEFAULT_LEGACY_CHARSET);
    private boolean forceLegacyCharsetForNonUnicodeEntries = false;

    @Override
    public Set<MediaType> getSupportedTypes(ParseContext context) {
        return SUPPORTED_TYPES;
    }

    @Field
    public void setLegacyCharset(String legacyCharset) {
        this.legacyCharset = ArchiveFilenameEncodingUtil.toCharset(legacyCharset);
    }

    @Field
    public void setForceLegacyCharsetForNonUnicodeEntries(
            boolean forceLegacyCharsetForNonUnicodeEntries) {
        this.forceLegacyCharsetForNonUnicodeEntries = forceLegacyCharsetForNonUnicodeEntries;
    }

    @Override
    public void parse(InputStream stream, ContentHandler handler, Metadata metadata,
                      ParseContext context) throws IOException, SAXException, TikaException {
        try (TemporaryResources tmp = new TemporaryResources()) {
            TikaInputStream tis = TikaInputStream.get(stream, tmp, metadata);
            String mediaType = metadata.get(Metadata.CONTENT_TYPE);
            try {
                if (isRar5(mediaType)) {
                    parseWithExternalUnrar(tis.getFile().toPath(), handler, metadata, context);
                    return;
                }
                parseWithJunrar(tis.getFile().toPath(), handler, metadata, context);
            } catch (UnsupportedRarV5Exception e) {
                parseWithExternalUnrar(tis.getFile().toPath(), handler, metadata, context);
            }
        }
    }

    private void parseWithJunrar(java.nio.file.Path rarFile, ContentHandler handler,
                                 Metadata metadata, ParseContext context)
            throws IOException, SAXException, TikaException, UnsupportedRarV5Exception {

        XHTMLContentHandler xhtml = new XHTMLContentHandler(handler, metadata);
        xhtml.startDocument();

        EmbeddedDocumentExtractor extractor =
                EmbeddedDocumentUtil.getEmbeddedDocumentExtractor(context);
        Archive rar = null;
        try {
            rar = new Archive(rarFile.toFile());

            if (rar.isEncrypted()) {
                throw new EncryptedDocumentException();
            }

            // Without this BodyContentHandler does not work.
            xhtml.element("div", " ");

            FileHeader header = rar.nextFileHeader();
            while (header != null && !Thread.currentThread().isInterrupted()) {
                if (!header.isDirectory()) {
                    String entryName = ArchiveFilenameEncodingUtil.repairRarEntryName(
                            header.getFileNameByteArray(), header.isUnicode(),
                            header.getFileName(), legacyCharset,
                            forceLegacyCharsetForNonUnicodeEntries);
                    Metadata entryData = ArchiveEntryMetadataUtil.handleEntryMetadata(entryName,
                            header.getCTime(), header.getMTime(), header.getFullUnpackSize(),
                            xhtml);
                    try (InputStream subFile = rar.getInputStream(header)) {
                        if (extractor.shouldParseEmbedded(entryData)) {
                            extractor.parseEmbedded(subFile, handler, entryData, true);
                        }
                    }
                }

                header = rar.nextFileHeader();
            }
        } catch (UnsupportedRarV5Exception e) {
            throw e;
        } catch (RarException e) {
            throw new TikaException("RarParser Exception", e);
        } finally {
            if (rar != null) {
                rar.close();
            }
            xhtml.endDocument();
        }
    }

    private void parseWithExternalUnrar(Path rarFile, ContentHandler handler, Metadata metadata,
                                        ParseContext context)
            throws IOException, SAXException, TikaException {

        XHTMLContentHandler xhtml = new XHTMLContentHandler(handler, metadata);
        xhtml.startDocument();

        EmbeddedDocumentExtractor extractor =
                EmbeddedDocumentUtil.getEmbeddedDocumentExtractor(context);
        Path extractDir = Files.createTempDirectory("cloudreve-rar-");
        try {
            extractWithUnrar(rarFile, extractDir);

            // Without this BodyContentHandler does not work.
            xhtml.element("div", " ");

            List<Path> extractedFiles;
            try (Stream<Path> stream = Files.walk(extractDir)) {
                extractedFiles = stream.filter(Files::isRegularFile).sorted().toList();
            }

            for (Path extractedFile : extractedFiles) {
                String entryName = extractDir.relativize(extractedFile).toString().replace('\\', '/');
                BasicFileAttributes attributes =
                        Files.readAttributes(extractedFile, BasicFileAttributes.class);
                Metadata entryData = ArchiveEntryMetadataUtil.handleEntryMetadata(entryName,
                        toDate(attributes.creationTime().toMillis()),
                        toDate(attributes.lastModifiedTime().toMillis()),
                        attributes.size(), xhtml);
                try (InputStream subFile = TikaInputStream.get(extractedFile, entryData)) {
                    if (extractor.shouldParseEmbedded(entryData)) {
                        extractor.parseEmbedded(subFile, handler, entryData, true);
                    }
                }
            }
        } finally {
            deleteRecursively(extractDir);
            xhtml.endDocument();
        }
    }

    private void extractWithUnrar(Path rarFile, Path extractDir) throws IOException, TikaException {
        ProcessBuilder pb = new ProcessBuilder(UNRAR_BINARY, "-x", rarFile.toString(),
                extractDir.toString());
        pb.redirectErrorStream(true);
        applyLocaleEnvironment(pb);

        Process process = pb.start();
        String output;
        try (InputStream input = process.getInputStream();
             ByteArrayOutputStream buffer = new ByteArrayOutputStream()) {
            input.transferTo(buffer);
            output = buffer.toString(StandardCharsets.UTF_8);
        }

        try {
            int exitCode = process.waitFor();
            if (exitCode != 0) {
                if (output.toLowerCase().contains("password")) {
                    throw new EncryptedDocumentException();
                }
                throw new TikaException("unrar-free failed with exit code " + exitCode +
                        (output.isBlank() ? "" : ": " + output.strip()));
            }
        } catch (InterruptedException e) {
            Thread.currentThread().interrupt();
            throw new TikaException("Interrupted while waiting for unrar-free", e);
        }
    }

    private static void applyLocaleEnvironment(ProcessBuilder pb) {
        boolean hasLocale = false;
        hasLocale |= copyEnvIfPresent(pb, "LANG");
        hasLocale |= copyEnvIfPresent(pb, "LC_ALL");
        copyEnvIfPresent(pb, "LANGUAGE");

        if (!hasLocale) {
            pb.environment().put("LANG", UTF8_FALLBACK_LOCALE);
            pb.environment().put("LC_ALL", UTF8_FALLBACK_LOCALE);
        }
    }

    private static boolean copyEnvIfPresent(ProcessBuilder pb, String key) {
        String value = System.getenv(key);
        if (value == null || value.isBlank()) {
            return false;
        }

        pb.environment().put(key, value);
        return true;
    }

    private static void deleteRecursively(Path path) {
        if (path == null) {
            return;
        }

        try (Stream<Path> stream = Files.walk(path)) {
            stream.sorted(Comparator.reverseOrder()).forEach(file -> {
                try {
                    Files.deleteIfExists(file);
                } catch (IOException ignored) {
                    // Best-effort cleanup for temporary extraction directory.
                }
            });
        } catch (IOException ignored) {
            // Best-effort cleanup for temporary extraction directory.
        }
    }

    private static Date toDate(long millis) {
        return millis <= 0 ? null : new Date(millis);
    }

    private static boolean isRar5(String mediaType) {
        return mediaType != null && mediaType.contains("version=5");
    }
}
