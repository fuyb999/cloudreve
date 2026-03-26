package org.cloudreve.tika.parser.pkg;

import static org.junit.jupiter.api.Assertions.assertEquals;

import java.nio.charset.Charset;
import java.nio.charset.StandardCharsets;

import org.junit.jupiter.api.Test;

class ArchiveFilenameEncodingUtilTest {

    private static final Charset GB18030 = Charset.forName("GB18030");
    private static final Charset CP437 = Charset.forName("IBM437");

    @Test
    void repairsBoxDrawingZipMojibake() {
        byte[] rawName = "中文/测试.txt".getBytes(GB18030);
        String currentName = new String(rawName, CP437);

        String repaired = ArchiveFilenameEncodingUtil.repairLegacyEncodedName(rawName,
                currentName, GB18030, false);

        assertEquals("中文/测试.txt", repaired);
    }

    @Test
    void repairsLatinSupplementMojibake() {
        byte[] rawName = "中文.doc".getBytes(GB18030);
        String currentName = new String(rawName, StandardCharsets.ISO_8859_1);

        String repaired = ArchiveFilenameEncodingUtil.repairLegacyEncodedName(rawName,
                currentName, GB18030, false);

        assertEquals("中文.doc", repaired);
    }

    @Test
    void keepsAsciiNamesUntouched() {
        byte[] rawName = "docs/readme.txt".getBytes(StandardCharsets.US_ASCII);
        String currentName = "docs/readme.txt";

        String repaired = ArchiveFilenameEncodingUtil.repairLegacyEncodedName(rawName,
                currentName, GB18030, false);

        assertEquals("docs/readme.txt", repaired);
    }

    @Test
    void canForceLegacyCharsetForNonUnicodeEntries() {
        byte[] rawName = "目录/文件.txt".getBytes(GB18030);
        String currentName = "garbled-name";

        String repaired = ArchiveFilenameEncodingUtil.repairLegacyEncodedName(rawName,
                currentName, GB18030, true);

        assertEquals("目录/文件.txt", repaired);
    }

    @Test
    void repairsNonUnicodeRarNames() {
        byte[] rawName = "中文/测试.txt".getBytes(GB18030);
        String currentName = new String(rawName, CP437);

        String repaired = ArchiveFilenameEncodingUtil.repairRarEntryName(rawName,
                false, currentName, GB18030, false);

        assertEquals("中文/测试.txt", repaired);
    }

    @Test
    void keepsReadableNonUnicodeRarNamesUntouched() {
        byte[] rawName = "文本文档.txt".getBytes(StandardCharsets.UTF_8);
        String currentName = "文本文档.txt";

        String repaired = ArchiveFilenameEncodingUtil.repairRarEntryName(rawName,
                false, currentName, GB18030, false);

        assertEquals("文本文档.txt", repaired);
    }
}
