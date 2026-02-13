package com.linlay.springaiagw.agent.runtime;

/**
 * Incrementally extracts JSON string fields from streamed chunks.
 */
final class StreamingJsonFieldExtractor {

    private final StringBuilder raw = new StringBuilder();
    private final StringBuilder emittedFinalText = new StringBuilder();
    private final StringBuilder emittedReasoningSummary = new StringBuilder();

    FieldDeltas append(String chunk) {
        if (chunk == null || chunk.isEmpty()) {
            return FieldDeltas.EMPTY;
        }
        raw.append(chunk);
        String reasoningDelta = extractDelta("reasoningSummary", emittedReasoningSummary);
        String finalTextDelta = extractDelta("finalText", emittedFinalText);
        return new FieldDeltas(reasoningDelta, finalTextDelta);
    }

    String rawText() {
        return raw.toString();
    }

    String finalText() {
        return emittedFinalText.toString();
    }

    String reasoningSummary() {
        return emittedReasoningSummary.toString();
    }

    private String extractDelta(String key, StringBuilder emitted) {
        String current = extractFieldValue(key);
        if (current.isEmpty() || current.length() <= emitted.length()) {
            return "";
        }
        String delta = current.substring(emitted.length());
        emitted.append(delta);
        return delta;
    }

    private String extractFieldValue(String key) {
        if (raw.isEmpty() || key == null || key.isBlank()) {
            return "";
        }
        String source = raw.toString();
        int keyStart = source.indexOf("\"" + key + "\"");
        if (keyStart < 0) {
            return "";
        }

        int colon = source.indexOf(':', keyStart + key.length() + 2);
        if (colon < 0) {
            return "";
        }

        int valueStart = skipWhitespace(source, colon + 1);
        if (valueStart >= source.length() || source.charAt(valueStart) != '"') {
            return "";
        }

        StringBuilder value = new StringBuilder();
        int index = valueStart + 1;
        while (index < source.length()) {
            char ch = source.charAt(index);
            if (ch == '"') {
                return value.toString();
            }
            if (ch != '\\') {
                value.append(ch);
                index++;
                continue;
            }
            if (index + 1 >= source.length()) {
                return value.toString();
            }

            char escaped = source.charAt(index + 1);
            switch (escaped) {
                case '"', '\\', '/' -> value.append(escaped);
                case 'b' -> value.append('\b');
                case 'f' -> value.append('\f');
                case 'n' -> value.append('\n');
                case 'r' -> value.append('\r');
                case 't' -> value.append('\t');
                case 'u' -> {
                    if (index + 5 >= source.length()) {
                        return value.toString();
                    }
                    String hex = source.substring(index + 2, index + 6);
                    if (!isHex(hex)) {
                        value.append("\\u").append(hex);
                    } else {
                        value.append((char) Integer.parseInt(hex, 16));
                    }
                    index += 4;
                }
                default -> value.append(escaped);
            }
            index += 2;
        }

        return value.toString();
    }

    private int skipWhitespace(String text, int start) {
        int index = start;
        while (index < text.length() && Character.isWhitespace(text.charAt(index))) {
            index++;
        }
        return index;
    }

    private boolean isHex(String text) {
        if (text == null || text.length() != 4) {
            return false;
        }
        for (int i = 0; i < text.length(); i++) {
            char ch = text.charAt(i);
            boolean digit = ch >= '0' && ch <= '9';
            boolean upper = ch >= 'A' && ch <= 'F';
            boolean lower = ch >= 'a' && ch <= 'f';
            if (!digit && !upper && !lower) {
                return false;
            }
        }
        return true;
    }

    record FieldDeltas(String reasoningDelta, String finalTextDelta) {
        private static final FieldDeltas EMPTY = new FieldDeltas("", "");
    }
}
