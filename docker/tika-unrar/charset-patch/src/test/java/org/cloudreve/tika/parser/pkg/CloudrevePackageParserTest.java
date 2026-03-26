package org.cloudreve.tika.parser.pkg;

import static org.junit.jupiter.api.Assertions.assertEquals;

import java.io.ByteArrayInputStream;
import java.io.ByteArrayOutputStream;
import java.io.IOException;
import java.nio.charset.StandardCharsets;
import java.util.ArrayList;
import java.util.List;

import org.apache.commons.compress.archivers.zip.ZipArchiveEntry;
import org.apache.commons.compress.archivers.zip.ZipArchiveOutputStream;
import org.junit.jupiter.api.Test;
import org.xml.sax.ContentHandler;
import org.xml.sax.helpers.DefaultHandler;

import org.apache.tika.extractor.EmbeddedDocumentExtractor;
import org.apache.tika.metadata.Metadata;
import org.apache.tika.metadata.TikaCoreProperties;
import org.apache.tika.parser.ParseContext;

class CloudrevePackageParserTest {

    @Test
    void parsesGbkZipNamesAsChinese() throws Exception {
        byte[] zipBytes = buildZip("中文/测试.txt", "GBK", false,
                "hello".getBytes(StandardCharsets.UTF_8));

        CloudrevePackageParser parser = new CloudrevePackageParser();
        parser.setForceLegacyCharsetForNonUnicodeEntries(true);

        Metadata metadata = new Metadata();
        metadata.set(Metadata.CONTENT_TYPE, "application/zip");

        CollectingEmbeddedDocumentExtractor extractor = new CollectingEmbeddedDocumentExtractor();
        ParseContext context = new ParseContext();
        context.set(EmbeddedDocumentExtractor.class, extractor);

        ContentHandler handler = new DefaultHandler();
        parser.parse(new ByteArrayInputStream(zipBytes), handler, metadata, context);

        assertEquals(List.of("中文/测试.txt"), extractor.names);
    }

    @Test
    void keepsUtf8ZipNamesAsChinese() throws Exception {
        byte[] zipBytes = buildZip("中文/测试.txt", "UTF-8", true,
                "hello".getBytes(StandardCharsets.UTF_8));

        CloudrevePackageParser parser = new CloudrevePackageParser();
        parser.setForceLegacyCharsetForNonUnicodeEntries(true);

        Metadata metadata = new Metadata();
        metadata.set(Metadata.CONTENT_TYPE, "application/zip");

        CollectingEmbeddedDocumentExtractor extractor = new CollectingEmbeddedDocumentExtractor();
        ParseContext context = new ParseContext();
        context.set(EmbeddedDocumentExtractor.class, extractor);

        ContentHandler handler = new DefaultHandler();
        parser.parse(new ByteArrayInputStream(zipBytes), handler, metadata, context);

        assertEquals(List.of("中文/测试.txt"), extractor.names);
    }

    @Test
    void parsesGb2312ZipNamesAsChinese() throws Exception {
        byte[] zipBytes = buildZip("中文/测试.txt", "GB2312", false,
                "hello".getBytes(StandardCharsets.UTF_8));

        CloudrevePackageParser parser = new CloudrevePackageParser();
        parser.setForceLegacyCharsetForNonUnicodeEntries(true);

        Metadata metadata = new Metadata();
        metadata.set(Metadata.CONTENT_TYPE, "application/zip");

        CollectingEmbeddedDocumentExtractor extractor = new CollectingEmbeddedDocumentExtractor();
        ParseContext context = new ParseContext();
        context.set(EmbeddedDocumentExtractor.class, extractor);

        ContentHandler handler = new DefaultHandler();
        parser.parse(new ByteArrayInputStream(zipBytes), handler, metadata, context);

        assertEquals(List.of("中文/测试.txt"), extractor.names);
    }

    private static byte[] buildZip(String entryName, String encoding, boolean useUtf8Flag,
                                   byte[] content) throws IOException {
        ByteArrayOutputStream output = new ByteArrayOutputStream();
        try (ZipArchiveOutputStream zip = new ZipArchiveOutputStream(output)) {
            zip.setEncoding(encoding);
            zip.setUseLanguageEncodingFlag(useUtf8Flag);
            zip.setCreateUnicodeExtraFields(ZipArchiveOutputStream.UnicodeExtraFieldPolicy.NEVER);

            ZipArchiveEntry entry = new ZipArchiveEntry(entryName);
            zip.putArchiveEntry(entry);
            zip.write(content);
            zip.closeArchiveEntry();
            zip.finish();
        }
        return output.toByteArray();
    }

    private static final class CollectingEmbeddedDocumentExtractor
            implements EmbeddedDocumentExtractor {
        private final List<String> names = new ArrayList<>();

        @Override
        public boolean shouldParseEmbedded(Metadata metadata) {
            return true;
        }

        @Override
        public void parseEmbedded(java.io.InputStream stream, ContentHandler handler,
                                  Metadata metadata, boolean outputHtml) {
            names.add(metadata.get(TikaCoreProperties.RESOURCE_NAME_KEY));
        }
    }
}
