package org.cloudreve.tika.parser.pkg;

import static org.junit.jupiter.api.Assertions.assertEquals;

import java.io.ByteArrayInputStream;
import java.util.ArrayList;
import java.util.Base64;
import java.util.List;
import java.util.Map;

import org.junit.jupiter.api.Test;
import org.xml.sax.ContentHandler;
import org.xml.sax.helpers.DefaultHandler;

import org.apache.tika.extractor.EmbeddedDocumentExtractor;
import org.apache.tika.metadata.Metadata;
import org.apache.tika.metadata.TikaCoreProperties;
import org.apache.tika.parser.ParseContext;

class CloudreveRarParserTest {

    private static final byte[] WINRAR_RAR4 =
            Base64.getDecoder().decode(
                    "UmFyIRoHAM+QcwAADQAAAAAAAACc+3Qgkj8AAAAAAAAAAAACAAAAADpZczwdMBoAIAAAAM7Esb7OxLW1LnR4dABlZocsZ4djaAAudHh0ALCUNXfEPXsAQAcA");

    private static final byte[] FILE_ROLLER_RAR4 =
            Base64.getDecoder().decode(
                    "UmFyIRoHAM+QcwAADQAAAAAAAACOiXQggDAACAAAAAAAAAADAAAAADpZczwdMxAA/4EAAOaWh+acrOaWh+ahoy50eHQAv4hn9qn/1MQ9ewBABwA=");

    @Test
    void keepsUnicodeRar4NamesAsChinese() throws Exception {
        assertEquals(List.of("文本文档.txt"), parseEntryNames(WINRAR_RAR4));
    }

    @Test
    void keepsReadableNonUnicodeRar4NamesAsChinese() throws Exception {
        assertEquals(List.of("文本文档.txt"), parseEntryNames(FILE_ROLLER_RAR4));
    }

    @Test
    void appliesZhCnUtf8FallbackLocaleWhenMissing() {
        ProcessBuilder pb = new ProcessBuilder("true");
        pb.environment().clear();

        CloudreveRarParser.applyLocaleEnvironment(pb, Map.of());

        assertEquals("zh_CN.UTF-8", pb.environment().get("LANG"));
        assertEquals("zh_CN.UTF-8", pb.environment().get("LC_ALL"));
        assertEquals("zh_CN:zh", pb.environment().get("LANGUAGE"));
    }

    @Test
    void copiesExistingLocaleEnvironmentWhenPresent() {
        ProcessBuilder pb = new ProcessBuilder("true");
        pb.environment().clear();

        CloudreveRarParser.applyLocaleEnvironment(pb, Map.of(
                "LANG", "ja_JP.UTF-8",
                "LC_ALL", "ja_JP.UTF-8",
                "LANGUAGE", "ja_JP:ja"
        ));

        assertEquals("ja_JP.UTF-8", pb.environment().get("LANG"));
        assertEquals("ja_JP.UTF-8", pb.environment().get("LC_ALL"));
        assertEquals("ja_JP:ja", pb.environment().get("LANGUAGE"));
    }

    private static List<String> parseEntryNames(byte[] rarBytes) throws Exception {
        CloudreveRarParser parser = new CloudreveRarParser();
        parser.setLegacyCharset("GB18030");
        parser.setForceLegacyCharsetForNonUnicodeEntries(false);

        Metadata metadata = new Metadata();
        metadata.set(Metadata.CONTENT_TYPE, "application/vnd.rar");

        CollectingEmbeddedDocumentExtractor extractor = new CollectingEmbeddedDocumentExtractor();
        ParseContext context = new ParseContext();
        context.set(EmbeddedDocumentExtractor.class, extractor);

        ContentHandler handler = new DefaultHandler();
        parser.parse(new ByteArrayInputStream(rarBytes), handler, metadata, context);
        return extractor.names;
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
