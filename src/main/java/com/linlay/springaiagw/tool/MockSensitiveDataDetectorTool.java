package com.linlay.springaiagw.tool;

import com.fasterxml.jackson.databind.JsonNode;
import com.fasterxml.jackson.databind.node.ObjectNode;
import org.springframework.stereotype.Component;

import java.util.List;
import java.util.Map;
import java.util.regex.Pattern;

@Component
public class MockSensitiveDataDetectorTool extends AbstractDeterministicTool {

    private static final Pattern CHINA_MOBILE = Pattern.compile("(?<!\\d)1[3-9]\\d{9}(?!\\d)");
    private static final Pattern EMAIL = Pattern.compile("(?i)\\b[A-Z0-9._%+-]+@[A-Z0-9.-]+\\.[A-Z]{2,}\\b");
    private static final Pattern CHINA_ID_CARD = Pattern.compile("(?i)\\b[1-9]\\d{5}(18|19|20)\\d{2}(0[1-9]|1[0-2])(0[1-9]|[12]\\d|3[01])\\d{3}[0-9X]\\b");
    private static final Pattern BANK_CARD = Pattern.compile("(?<!\\d)(?:\\d[ -]?){16,19}(?!\\d)");
    private static final Pattern OPENAI_KEY = Pattern.compile("\\bsk-[A-Za-z0-9_-]{16,}\\b");
    private static final Pattern AWS_KEY = Pattern.compile("\\bAKIA[0-9A-Z]{16}\\b");

    private static final List<Rule> RULES = List.of(
            new Rule("手机号", CHINA_MOBILE),
            new Rule("邮箱", EMAIL),
            new Rule("身份证号", CHINA_ID_CARD),
            new Rule("银行卡号", BANK_CARD),
            new Rule("OpenAI Key", OPENAI_KEY),
            new Rule("AWS Access Key", AWS_KEY)
    );

    @Override
    public String name() {
        return "mock_sensitive_data_detector";
    }

    @Override
    public String description() {
        return "[MOCK] 检测超长文本中是否包含敏感数据，返回有/没有和简短说明";
    }

    @Override
    public Map<String, Object> parametersSchema() {
        return Map.of(
                "type", "object",
                "properties", Map.of(
                        "text", Map.of(
                                "type", "string",
                                "description", "待检测文本，推荐使用 text 字段"
                        ),
                        "content", Map.of("type", "string"),
                        "message", Map.of("type", "string"),
                        "query", Map.of("type", "string"),
                        "document", Map.of("type", "string"),
                        "input", Map.of("type", "string")
                ),
                "additionalProperties", false
        );
    }

    @Override
    public JsonNode invoke(Map<String, Object> args) {
        String text = readText(args);
        ObjectNode root = OBJECT_MAPPER.createObjectNode();
        root.put("tool", name());

        if (text.isBlank()) {
            root.put("hasSensitiveData", false);
            root.put("result", "没有敏感数据");
            root.put("description", "未检测到可分析文本。");
            return root;
        }

        for (Rule rule : RULES) {
            if (rule.pattern().matcher(text).find()) {
                root.put("hasSensitiveData", true);
                root.put("result", "有敏感数据");
                root.put("description", "检测到疑似" + rule.label() + "信息，建议脱敏后再传输。");
                return root;
            }
        }

        root.put("hasSensitiveData", false);
        root.put("result", "没有敏感数据");
        root.put("description", "未发现明显敏感字段特征。");
        return root;
    }

    private String readText(Map<String, Object> args) {
        Object value = firstNonNull(
                args.get("text"),
                args.get("content"),
                args.get("message"),
                args.get("query"),
                args.get("document"),
                args.get("input")
        );
        return value == null ? "" : String.valueOf(value);
    }

    private Object firstNonNull(Object... values) {
        for (Object value : values) {
            if (value != null) {
                return value;
            }
        }
        return null;
    }

    private record Rule(String label, Pattern pattern) {
    }
}
