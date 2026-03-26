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

import static org.apache.tika.detect.zip.PackageConstants.AR;
import static org.apache.tika.detect.zip.PackageConstants.ARJ;
import static org.apache.tika.detect.zip.PackageConstants.CPIO;
import static org.apache.tika.detect.zip.PackageConstants.DUMP;
import static org.apache.tika.detect.zip.PackageConstants.JAR;
import static org.apache.tika.detect.zip.PackageConstants.SEVENZ;
import static org.apache.tika.detect.zip.PackageConstants.TAR;
import static org.apache.tika.detect.zip.PackageConstants.ZIP;

import java.io.BufferedInputStream;
import java.io.IOException;
import java.io.InputStream;
import java.nio.charset.Charset;
import java.util.Collections;
import java.util.HashSet;
import java.util.Set;
import java.util.concurrent.atomic.AtomicInteger;
import java.util.zip.ZipEntry;

import org.apache.commons.compress.PasswordRequiredException;
import org.apache.commons.compress.archivers.ArchiveEntry;
import org.apache.commons.compress.archivers.ArchiveException;
import org.apache.commons.compress.archivers.ArchiveInputStream;
import org.apache.commons.compress.archivers.ArchiveStreamFactory;
import org.apache.commons.compress.archivers.StreamingNotSupportedException;
import org.apache.commons.compress.archivers.ar.ArArchiveInputStream;
import org.apache.commons.compress.archivers.cpio.CpioArchiveInputStream;
import org.apache.commons.compress.archivers.dump.DumpArchiveInputStream;
import org.apache.commons.compress.archivers.jar.JarArchiveInputStream;
import org.apache.commons.compress.archivers.sevenz.SevenZFile;
import org.apache.commons.compress.archivers.tar.TarArchiveInputStream;
import org.apache.commons.compress.archivers.zip.UnsupportedZipFeatureException;
import org.apache.commons.compress.archivers.zip.UnsupportedZipFeatureException.Feature;
import org.apache.commons.compress.archivers.zip.ZipArchiveEntry;
import org.apache.commons.compress.archivers.zip.ZipArchiveInputStream;
import org.apache.commons.io.input.CloseShieldInputStream;
import org.apache.commons.io.input.UnsynchronizedByteArrayInputStream;
import org.xml.sax.ContentHandler;
import org.xml.sax.SAXException;

import org.apache.tika.config.Field;
import org.apache.tika.detect.EncodingDetector;
import org.apache.tika.exception.EncryptedDocumentException;
import org.apache.tika.exception.TikaException;
import org.apache.tika.extractor.EmbeddedDocumentExtractor;
import org.apache.tika.extractor.EmbeddedDocumentUtil;
import org.apache.tika.io.TemporaryResources;
import org.apache.tika.io.TikaInputStream;
import org.apache.tika.metadata.Metadata;
import org.apache.tika.mime.MediaType;
import org.apache.tika.parser.AbstractEncodingDetectorParser;
import org.apache.tika.parser.ParseContext;
import org.apache.tika.parser.PasswordProvider;
import org.apache.tika.sax.XHTMLContentHandler;

/**
 * Package parser with legacy Chinese ZIP entry filename support.
 */
public class CloudrevePackageParser extends AbstractEncodingDetectorParser {

    static final Set<MediaType> PACKAGE_SPECIALIZATIONS = loadPackageSpecializations();

    private static final long serialVersionUID = -5331043266963888708L;
    private static final Set<MediaType> SUPPORTED_TYPES =
            MediaType.set(ZIP, JAR, AR, ARJ, CPIO, DUMP, TAR, SEVENZ);
    private static final int MARK_LIMIT = 100 * 1024 * 1024;
    private static final int MIN_BYTES_FOR_DETECTING_CHARSET = 100;

    private boolean detectCharsetsInEntryNames = true;
    private Charset legacyCharset =
            ArchiveFilenameEncodingUtil.toCharset(ArchiveFilenameEncodingUtil.DEFAULT_LEGACY_CHARSET);
    private boolean forceLegacyCharsetForNonUnicodeEntries = false;

    static final Set<MediaType> loadPackageSpecializations() {
        Set<MediaType> zipSpecializations = new HashSet<>();
        for (String mediaTypeString : new String[]{
                "application/bizagi-modeler", "application/epub+zip",
                "application/hwp+zip", "application/java-archive",
                "application/vnd.adobe.air-application-installer-package+zip",
                "application/vnd.android.package-archive", "application/vnd.apple.iwork",
                "application/vnd.apple.keynote", "application/vnd.apple.numbers",
                "application/vnd.apple.pages", "application/vnd.apple.unknown.13",
                "application/vnd.etsi.asic-e+zip", "application/vnd.etsi.asic-s+zip",
                "application/vnd.google-earth.kmz", "application/vnd.mindjet.mindmanager",
                "application/vnd.ms-excel.addin.macroenabled.12",
                "application/vnd.ms-excel.sheet.binary.macroenabled.12",
                "application/vnd.ms-excel.sheet.macroenabled.12",
                "application/vnd.ms-excel.template.macroenabled.12",
                "application/vnd.ms-powerpoint.addin.macroenabled.12",
                "application/vnd.ms-powerpoint.presentation.macroenabled.12",
                "application/vnd.ms-powerpoint.slide.macroenabled.12",
                "application/vnd.ms-powerpoint.slideshow.macroenabled.12",
                "application/vnd.ms-powerpoint.template.macroenabled.12",
                "application/vnd.ms-visio.drawing",
                "application/vnd.ms-visio.drawing.macroenabled.12",
                "application/vnd.ms-visio.stencil",
                "application/vnd.ms-visio.stencil.macroenabled.12",
                "application/vnd.ms-visio.template",
                "application/vnd.ms-visio.template.macroenabled.12",
                "application/vnd.ms-word.document.macroenabled.12",
                "application/vnd.ms-word.template.macroenabled.12",
                "application/vnd.ms-xpsdocument",
                "application/vnd.oasis.opendocument.formula",
                "application/vnd.openxmlformats-officedocument.presentationml.presentation",
                "application/vnd.openxmlformats-officedocument.presentationml.slide",
                "application/vnd.openxmlformats-officedocument.presentationml.slideshow",
                "application/vnd.openxmlformats-officedocument.presentationml.template",
                "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet",
                "application/vnd.openxmlformats-officedocument.spreadsheetml.template",
                "application/vnd.openxmlformats-officedocument.wordprocessingml.document",
                "application/vnd.openxmlformats-officedocument.wordprocessingml.template",
                "application/x-ibooks+zip", "application/x-itunes-ipa",
                "application/x-tika-iworks-protected",
                "application/x-tika-java-enterprise-archive",
                "application/x-tika-java-web-archive", "application/x-tika-ooxml",
                "application/x-tika-visio-ooxml", "application/x-xliff+zip",
                "application/x-xmind", "model/vnd.dwfx+xps",
                "application/vnd.sun.xml.calc", "application/vnd.sun.xml.writer",
                "application/vnd.sun.xml.writer.template",
                "application/vnd.sun.xml.draw", "application/vnd.sun.xml.impress",
                "application/vnd.openofficeorg.autotext",
                "application/vnd.oasis.opendocument.graphics-template",
                "application/vnd.oasis.opendocument.text-web",
                "application/vnd.oasis.opendocument.spreadsheet-template",
                "application/vnd.oasis.opendocument.graphics",
                "application/vnd.oasis.opendocument.image-template",
                "application/vnd.oasis.opendocument.text",
                "application/vnd.oasis.opendocument.text-template",
                "application/vnd.oasis.opendocument.presentation",
                "application/vnd.oasis.opendocument.chart",
                "application/vnd.openofficeorg.extension",
                "application/vnd.oasis.opendocument.spreadsheet",
                "application/vnd.oasis.opendocument.image",
                "application/vnd.oasis.opendocument.formula-template",
                "application/vnd.oasis.opendocument.presentation-template",
                "application/vnd.oasis.opendocument.chart-template",
                "application/vnd.oasis.opendocument.text-master",
                "application/vnd.adobe.indesign-idml-package",
                "application/x-gtar", "application/x-wacz",
                "application/x-vnd.datapackage+zip"
        }) {
            zipSpecializations.add(MediaType.parse(mediaTypeString));
        }
        return Collections.unmodifiableSet(zipSpecializations);
    }

    @Deprecated
    static MediaType getMediaType(ArchiveInputStream stream) {
        if (stream instanceof JarArchiveInputStream) {
            return JAR;
        } else if (stream instanceof ZipArchiveInputStream) {
            return ZIP;
        } else if (stream instanceof ArArchiveInputStream) {
            return AR;
        } else if (stream instanceof CpioArchiveInputStream) {
            return CPIO;
        } else if (stream instanceof DumpArchiveInputStream) {
            return DUMP;
        } else if (stream instanceof TarArchiveInputStream) {
            return TAR;
        } else if (stream instanceof SevenZWrapper) {
            return SEVENZ;
        } else {
            return MediaType.OCTET_STREAM;
        }
    }

    public CloudrevePackageParser() {
        super();
    }

    public CloudrevePackageParser(EncodingDetector encodingDetector) {
        super(encodingDetector);
    }

    @Override
    public Set<MediaType> getSupportedTypes(ParseContext context) {
        return SUPPORTED_TYPES;
    }

    @Override
    public void parse(InputStream stream, ContentHandler handler, Metadata metadata,
                      ParseContext context) throws IOException, SAXException, TikaException {
        if (!stream.markSupported()) {
            stream = new BufferedInputStream(stream);
        }

        TemporaryResources tmp = new TemporaryResources();
        try {
            parseArchive(stream, handler, metadata, context, tmp);
        } finally {
            tmp.close();
        }
    }

    private void parseArchive(InputStream stream, ContentHandler handler, Metadata metadata,
                              ParseContext context, TemporaryResources tmp)
            throws TikaException, IOException, SAXException {
        ArchiveInputStream ais;
        String encoding;
        try {
            ArchiveStreamFactory factory =
                    context.get(ArchiveStreamFactory.class, new ArchiveStreamFactory());
            encoding = factory.getEntryEncoding();
            ais = factory.createArchiveInputStream(CloseShieldInputStream.wrap(stream));
        } catch (StreamingNotSupportedException sne) {
            if (sne.getFormat().equals(ArchiveStreamFactory.SEVEN_Z)) {
                stream.reset();
                TikaInputStream tstream = TikaInputStream.get(stream, tmp, metadata);

                String password = null;
                PasswordProvider provider = context.get(PasswordProvider.class);
                if (provider != null) {
                    password = provider.getPassword(metadata);
                }

                SevenZFile sevenz;
                try {
                    SevenZFile.Builder builder = new SevenZFile.Builder().setFile(tstream.getFile());
                    if (password == null) {
                        sevenz = builder.get();
                    } else {
                        sevenz = builder.setPassword(password.toCharArray()).get();
                    }
                } catch (PasswordRequiredException e) {
                    throw new EncryptedDocumentException(e);
                }

                ais = new SevenZWrapper(sevenz);
                encoding = null;
            } else {
                throw new TikaException("Unknown non-streaming format " + sne.getFormat(), sne);
            }
        } catch (ArchiveException e) {
            throw new TikaException("Unable to unpack document stream", e);
        }

        updateMediaType(ais, metadata);
        EmbeddedDocumentExtractor extractor =
                EmbeddedDocumentUtil.getEmbeddedDocumentExtractor(context);

        XHTMLContentHandler xhtml = new XHTMLContentHandler(handler, metadata);
        xhtml.startDocument();

        stream.mark(MARK_LIMIT);
        AtomicInteger entryCount = new AtomicInteger();
        try {
            parseEntries(ais, metadata, extractor, xhtml, false, entryCount);
        } catch (UnsupportedZipFeatureException zfe) {
            if (zfe.getFeature() == Feature.DATA_DESCRIPTOR) {
                ais.close();
                stream.reset();
                ais = new ZipArchiveInputStream(CloseShieldInputStream.wrap(stream), encoding, true,
                        true);
                parseEntries(ais, metadata, extractor, xhtml, true, entryCount);
            }
        } finally {
            ais.close();
            tmp.close();
            xhtml.endDocument();
        }
    }

    private void parseEntries(ArchiveInputStream ais, Metadata metadata,
                              EmbeddedDocumentExtractor extractor, XHTMLContentHandler xhtml,
                              boolean shouldUseDataDescriptor, AtomicInteger entryCount)
            throws TikaException, IOException, SAXException {
        try {
            ArchiveEntry entry = ais.getNextEntry();
            while (entry != null) {
                if (shouldUseDataDescriptor && entryCount.get() > 0) {
                    entryCount.decrementAndGet();
                    entry = ais.getNextEntry();
                    continue;
                }

                if (!entry.isDirectory()) {
                    parseEntry(ais, entry, extractor, metadata, xhtml);
                }

                if (!shouldUseDataDescriptor) {
                    entryCount.incrementAndGet();
                }

                entry = ais.getNextEntry();
            }
        } catch (UnsupportedZipFeatureException zfe) {
            if (zfe.getFeature() == Feature.ENCRYPTION) {
                throw new EncryptedDocumentException(zfe);
            }
            if (zfe.getFeature() == Feature.DATA_DESCRIPTOR) {
                throw zfe;
            }
            throw new TikaException("UnsupportedZipFeature", zfe);
        } catch (PasswordRequiredException pre) {
            throw new EncryptedDocumentException(pre);
        }
    }

    private void updateMediaType(ArchiveInputStream ais, Metadata metadata) {
        MediaType type = getMediaType(ais);
        if (type.equals(MediaType.OCTET_STREAM)) {
            return;
        }

        String incomingContentType = metadata.get(Metadata.CONTENT_TYPE);
        if (incomingContentType == null) {
            metadata.set(Metadata.CONTENT_TYPE, type.toString());
            return;
        }

        MediaType incomingMediaType = MediaType.parse(incomingContentType);
        if (incomingMediaType == null) {
            metadata.set(Metadata.CONTENT_TYPE, type.toString());
            return;
        }

        if (!PACKAGE_SPECIALIZATIONS.contains(incomingMediaType)) {
            metadata.set(Metadata.CONTENT_TYPE, type.toString());
        }
    }

    private void parseEntry(ArchiveInputStream archive, ArchiveEntry entry,
                            EmbeddedDocumentExtractor extractor, Metadata parentMetadata,
                            XHTMLContentHandler xhtml)
            throws SAXException, IOException, TikaException {
        String name = entry.getName();

        if (entry instanceof ZipArchiveEntry zipEntry) {
            if (!forceLegacyCharsetForNonUnicodeEntries && detectCharsetsInEntryNames) {
                byte[] entryName = zipEntry.getRawName();
                byte[] extendedEntryName = entryName;
                if (entryName != null && entryName.length > 0 &&
                        entryName.length < MIN_BYTES_FOR_DETECTING_CHARSET) {
                    int len = entryName.length *
                            (MIN_BYTES_FOR_DETECTING_CHARSET / entryName.length);
                    extendedEntryName = new byte[len];
                    for (int i = 0; i < len; i++) {
                        extendedEntryName[i] = entryName[i % entryName.length];
                    }
                }

                if (extendedEntryName != null && extendedEntryName.length > 0) {
                    Charset candidate = getEncodingDetector().detect(
                            UnsynchronizedByteArrayInputStream.builder()
                                    .setByteArray(extendedEntryName)
                                    .get(),
                            parentMetadata);
                    if (candidate != null) {
                        name = new String(zipEntry.getRawName(), candidate);
                    }
                }
            }

            name = ArchiveFilenameEncodingUtil.repairZipEntryName(zipEntry, name, legacyCharset,
                    forceLegacyCharsetForNonUnicodeEntries);
        }

        if (archive.canReadEntryData(entry)) {
            Metadata entryData = ArchiveEntryMetadataUtil.handleEntryMetadata(name, null,
                    entry.getLastModifiedDate(), entry.getSize(), xhtml);

            if (extractor.shouldParseEmbedded(entryData)) {
                TemporaryResources tmp = new TemporaryResources();
                try {
                    TikaInputStream tis = TikaInputStream.get(archive, tmp, entryData);
                    extractor.parseEmbedded(tis, xhtml, entryData, true);
                } finally {
                    tmp.dispose();
                }
            }
        } else {
            name = (name == null) ? "" : name;
            if (entry instanceof ZipArchiveEntry zipArchiveEntry) {
                boolean usesEncryption = zipArchiveEntry.getGeneralPurposeBit().usesEncryption();
                if (usesEncryption) {
                    EmbeddedDocumentUtil.recordEmbeddedStreamException(
                            new EncryptedDocumentException("stream (" + name + ") is encrypted"),
                            parentMetadata);
                }

                boolean usesDataDescriptor =
                        zipArchiveEntry.getGeneralPurposeBit().usesDataDescriptor();
                if (usesDataDescriptor && zipArchiveEntry.getMethod() == ZipEntry.STORED) {
                    throw new UnsupportedZipFeatureException(
                            UnsupportedZipFeatureException.Feature.DATA_DESCRIPTOR,
                            zipArchiveEntry);
                }
            } else {
                EmbeddedDocumentUtil.recordEmbeddedStreamException(
                        new TikaException("Can't read archive stream (" + name + ")"),
                        parentMetadata);
            }
            if (!name.isEmpty()) {
                xhtml.element("p", name);
            }
        }
    }

    private static class SevenZWrapper extends ArchiveInputStream {
        private final SevenZFile file;

        private SevenZWrapper(SevenZFile file) {
            this.file = file;
        }

        @Override
        public int read() throws IOException {
            return file.read();
        }

        @Override
        public int read(byte[] b) throws IOException {
            return file.read(b);
        }

        @Override
        public int read(byte[] b, int off, int len) throws IOException {
            return file.read(b, off, len);
        }

        @Override
        public ArchiveEntry getNextEntry() throws IOException {
            return file.getNextEntry();
        }

        @Override
        public void close() throws IOException {
            file.close();
        }
    }

    @Field
    public void setDetectCharsetsInEntryNames(boolean detectCharsetsInEntryNames) {
        this.detectCharsetsInEntryNames = detectCharsetsInEntryNames;
    }

    public boolean isDetectCharsetsInEntryNames() {
        return detectCharsetsInEntryNames;
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
}
