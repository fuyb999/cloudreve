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

import java.io.IOException;
import java.util.Date;

import org.xml.sax.SAXException;
import org.xml.sax.helpers.AttributesImpl;

import org.apache.tika.exception.TikaException;
import org.apache.tika.metadata.Metadata;
import org.apache.tika.metadata.TikaCoreProperties;
import org.apache.tika.sax.XHTMLContentHandler;

final class ArchiveEntryMetadataUtil {

    private ArchiveEntryMetadataUtil() {
    }

    static Metadata handleEntryMetadata(String name, Date createdAt, Date modifiedAt,
                                        Long size, XHTMLContentHandler xhtml)
            throws SAXException, IOException, TikaException {
        Metadata entryData = new Metadata();
        if (createdAt != null) {
            entryData.set(TikaCoreProperties.CREATED, createdAt);
        }
        if (modifiedAt != null) {
            entryData.set(TikaCoreProperties.MODIFIED, modifiedAt);
        }
        if (size != null) {
            entryData.set(Metadata.CONTENT_LENGTH, Long.toString(size));
        }
        if (name != null && !name.isEmpty()) {
            name = name.replace("\\", "/");
            entryData.set(TikaCoreProperties.RESOURCE_NAME_KEY, name);
            AttributesImpl attributes = new AttributesImpl();
            attributes.addAttribute("", "class", "class", "CDATA", "embedded");
            attributes.addAttribute("", "id", "id", "CDATA", name);
            xhtml.startElement("div", attributes);
            xhtml.endElement("div");

            entryData.set(TikaCoreProperties.EMBEDDED_RELATIONSHIP_ID, name);
        }
        return entryData;
    }
}
