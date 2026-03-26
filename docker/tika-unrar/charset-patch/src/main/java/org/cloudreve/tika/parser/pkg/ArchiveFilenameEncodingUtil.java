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

import java.nio.ByteBuffer;
import java.nio.CharBuffer;
import java.nio.charset.CharacterCodingException;
import java.nio.charset.Charset;
import java.nio.charset.CharsetDecoder;
import java.nio.charset.CodingErrorAction;

import org.apache.commons.compress.archivers.zip.GeneralPurposeBit;
import org.apache.commons.compress.archivers.zip.ZipArchiveEntry;

final class ArchiveFilenameEncodingUtil {

    static final String DEFAULT_LEGACY_CHARSET = "GB18030";

    private ArchiveFilenameEncodingUtil() {
    }

    static Charset toCharset(String charsetName) {
        String resolved = (charsetName == null || charsetName.isBlank()) ?
                DEFAULT_LEGACY_CHARSET : charsetName.trim();
        return Charset.forName(resolved);
    }

    static String repairZipEntryName(ZipArchiveEntry entry, String currentName, Charset legacyCharset,
                                     boolean forceLegacyCharsetForNonUnicodeEntries) {
        if (entry == null) {
            return currentName;
        }

        GeneralPurposeBit bit = entry.getGeneralPurposeBit();
        if (bit != null && bit.usesUTF8ForNames()) {
            return currentName;
        }

        return repairLegacyEncodedName(entry.getRawName(), currentName, legacyCharset,
                forceLegacyCharsetForNonUnicodeEntries);
    }

    static String repairRarEntryName(byte[] rawName, boolean unicode, String currentName,
                                     Charset legacyCharset,
                                     boolean forceLegacyCharsetForNonUnicodeEntries) {
        if (unicode) {
            return currentName;
        }

        return repairLegacyEncodedName(rawName, currentName, legacyCharset,
                forceLegacyCharsetForNonUnicodeEntries);
    }

    static String repairLegacyEncodedName(byte[] rawName, String currentName, Charset legacyCharset,
                                          boolean forceLegacyCharset) {
        if (rawName == null || rawName.length == 0 || legacyCharset == null) {
            return currentName;
        }

        String legacyDecoded = decodeStrict(rawName, legacyCharset);
        if (legacyDecoded == null || legacyDecoded.isEmpty()) {
            return currentName;
        }

        if (forceLegacyCharset || currentName == null || currentName.isEmpty()) {
            return legacyDecoded;
        }

        NameProfile current = NameProfile.analyze(currentName);
        NameProfile legacy = NameProfile.analyze(legacyDecoded);

        if (!legacy.isUsable()) {
            return currentName;
        }

        if (current.isCleanCjkName()) {
            return currentName;
        }

        if (current.isClearlyBroken()) {
            return legacyDecoded;
        }

        if (legacy.isCleanCjkName() && current.isLikelyMojibake()) {
            return legacyDecoded;
        }

        if (legacy.hasCjk() && !current.hasCjk() && legacy.score() > current.score() + 18) {
            return legacyDecoded;
        }

        return currentName;
    }

    private static String decodeStrict(byte[] rawName, Charset charset) {
        CharsetDecoder decoder = charset.newDecoder()
                .onMalformedInput(CodingErrorAction.REPORT)
                .onUnmappableCharacter(CodingErrorAction.REPORT);

        try {
            CharBuffer buffer = decoder.decode(ByteBuffer.wrap(rawName));
            return buffer.toString();
        } catch (CharacterCodingException e) {
            return null;
        }
    }

    static final class NameProfile {
        private final int score;
        private final int visibleCount;
        private final int cjkCount;
        private final int latinSupplementCount;
        private final int boxDrawingCount;
        private final int greekCount;
        private final int replacementCount;
        private final int controlCount;

        private NameProfile(int score, int visibleCount, int cjkCount, int latinSupplementCount,
                            int boxDrawingCount, int greekCount, int replacementCount,
                            int controlCount) {
            this.score = score;
            this.visibleCount = visibleCount;
            this.cjkCount = cjkCount;
            this.latinSupplementCount = latinSupplementCount;
            this.boxDrawingCount = boxDrawingCount;
            this.greekCount = greekCount;
            this.replacementCount = replacementCount;
            this.controlCount = controlCount;
        }

        static NameProfile analyze(String value) {
            int score = 0;
            int visibleCount = 0;
            int cjkCount = 0;
            int latinSupplementCount = 0;
            int boxDrawingCount = 0;
            int greekCount = 0;
            int replacementCount = 0;
            int controlCount = 0;

            if (value == null || value.isEmpty()) {
                return new NameProfile(-1000, 0, 0, 0, 0, 0, 0, 0);
            }

            for (int i = 0; i < value.length(); i += Character.charCount(value.codePointAt(i))) {
                int cp = value.codePointAt(i);
                if (cp == '/' || cp == '\\') {
                    continue;
                }

                visibleCount++;

                if (cp == 0xFFFD) {
                    replacementCount++;
                    score -= 80;
                    continue;
                }

                if (Character.isISOControl(cp)) {
                    controlCount++;
                    score -= 40;
                    continue;
                }

                if (isCjk(cp)) {
                    cjkCount++;
                    score += 12;
                    continue;
                }

                Character.UnicodeBlock block = Character.UnicodeBlock.of(cp);
                if (block == Character.UnicodeBlock.BOX_DRAWING ||
                        block == Character.UnicodeBlock.BLOCK_ELEMENTS) {
                    boxDrawingCount++;
                    score -= 12;
                    continue;
                }

                if (block == Character.UnicodeBlock.LATIN_1_SUPPLEMENT) {
                    latinSupplementCount++;
                    score -= 4;
                    continue;
                }

                Character.UnicodeScript script = Character.UnicodeScript.of(cp);
                if (script == Character.UnicodeScript.GREEK) {
                    greekCount++;
                    score -= 6;
                    continue;
                }

                if (cp >= 0x20 && cp <= 0x7E) {
                    score += 3;
                    continue;
                }

                if (Character.isLetterOrDigit(cp)) {
                    score += 2;
                    continue;
                }

                score += 1;
            }

            if (cjkCount > 0) {
                score += 16;
            }

            if (replacementCount == 0 && controlCount == 0 && boxDrawingCount == 0) {
                score += 6;
            }

            return new NameProfile(score, visibleCount, cjkCount, latinSupplementCount,
                    boxDrawingCount, greekCount, replacementCount, controlCount);
        }

        int score() {
            return score;
        }

        boolean hasCjk() {
            return cjkCount > 0;
        }

        boolean isUsable() {
            return visibleCount > 0 && replacementCount == 0 && controlCount == 0;
        }

        boolean isCleanCjkName() {
            return hasCjk() && replacementCount == 0 && controlCount == 0 &&
                    boxDrawingCount == 0;
        }

        boolean isClearlyBroken() {
            return replacementCount > 0 || controlCount > 0 || boxDrawingCount > 0;
        }

        boolean isLikelyMojibake() {
            if (isClearlyBroken()) {
                return true;
            }

            if (hasCjk() || visibleCount == 0) {
                return false;
            }

            return latinSupplementCount * 2 >= visibleCount ||
                    greekCount * 2 >= visibleCount;
        }

        private static boolean isCjk(int codePoint) {
            Character.UnicodeScript script = Character.UnicodeScript.of(codePoint);
            if (script == Character.UnicodeScript.HAN) {
                return true;
            }

            Character.UnicodeBlock block = Character.UnicodeBlock.of(codePoint);
            return block == Character.UnicodeBlock.CJK_SYMBOLS_AND_PUNCTUATION ||
                    block == Character.UnicodeBlock.HALFWIDTH_AND_FULLWIDTH_FORMS;
        }
    }
}
