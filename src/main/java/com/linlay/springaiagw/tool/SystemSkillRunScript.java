package com.linlay.springaiagw.tool;

import com.fasterxml.jackson.databind.JsonNode;
import com.fasterxml.jackson.databind.node.ObjectNode;
import com.linlay.springaiagw.skill.SkillCatalogProperties;
import org.springframework.beans.factory.annotation.Autowired;
import org.springframework.stereotype.Component;

import java.io.IOException;
import java.io.InputStream;
import java.io.InputStreamReader;
import java.nio.charset.StandardCharsets;
import java.nio.file.Files;
import java.nio.file.Path;
import java.util.ArrayList;
import java.util.List;
import java.util.Locale;
import java.util.Map;
import java.util.UUID;
import java.util.concurrent.TimeUnit;

@Component
public class SystemSkillRunScript extends AbstractDeterministicTool {

    private static final int DEFAULT_TIMEOUT_MS = 10_000;
    private static final int MAX_TIMEOUT_MS = 120_000;
    private static final int MAX_OUTPUT_CHARS = 8_000;
    private static final int MAX_INLINE_PYTHON_CHARS = 64 * 1024;
    private static final Path INLINE_SCRIPT_ROOT = Path.of("/tmp/agw-skill-inline");

    private final Path skillsRoot;
    private final String pythonCommand;
    private final String bashCommand;

    @Autowired
    public SystemSkillRunScript(SkillCatalogProperties properties) {
        this(Path.of(properties.getExternalDir()), "python3", "bash");
    }

    SystemSkillRunScript(Path skillsRoot) {
        this(skillsRoot, "python3", "bash");
    }

    SystemSkillRunScript(Path skillsRoot, String pythonCommand, String bashCommand) {
        this.skillsRoot = skillsRoot.toAbsolutePath().normalize();
        this.pythonCommand = pythonCommand;
        this.bashCommand = bashCommand;
    }

    @Override
    public String name() {
        return "_skill_run_script_";
    }

    @Override
    public JsonNode invoke(Map<String, Object> args) {
        ObjectNode result = OBJECT_MAPPER.createObjectNode();
        result.put("tool", name());
        result.put("skillsRoot", skillsRoot.toString());

        String skill = readText(args, "skill");
        if (skill == null) {
            return failure(result, "Missing argument: skill");
        }
        String script = readText(args, "script");
        String pythonCode = readCode(args, "pythonCode");
        if (script == null && pythonCode == null) {
            return failure(result, "Missing argument: script or pythonCode");
        }
        if (script != null && pythonCode != null) {
            return failure(result, "Arguments script and pythonCode are mutually exclusive.");
        }
        if (pythonCode != null && pythonCode.length() > MAX_INLINE_PYTHON_CHARS) {
            return failure(result, "pythonCode too long. Maximum length is " + MAX_INLINE_PYTHON_CHARS + " characters.");
        }

        ParseArgsResult parsedArgs = parseScriptArgs(args == null ? null : args.get("args"));
        if (parsedArgs.error != null) {
            return failure(result, parsedArgs.error);
        }

        int timeoutMs = parseTimeoutMs(args == null ? null : args.get("timeoutMs"));
        Path skillDir = skillsRoot.resolve(skill).normalize();
        if (!skillDir.startsWith(skillsRoot)) {
            return failure(result, "Illegal skill path: " + skill);
        }
        if (!Files.isDirectory(skillDir)) {
            return failure(result, "Skill directory not found: " + skill);
        }

        ExecutionTarget target;
        if (script != null) {
            Path scriptPath = resolveScriptPath(skillDir, script);
            if (scriptPath == null) {
                return failure(result, "Illegal script path: " + script);
            }
            if (!Files.isRegularFile(scriptPath)) {
                return failure(result, "Script file not found: " + script);
            }
            List<String> command = buildCommand(scriptPath, parsedArgs.values);
            if (command.isEmpty()) {
                return failure(result, "Unsupported script type. Only .py and .sh are allowed.");
            }
            target = new ExecutionTarget("file", scriptPath, command, null, null);
        } else {
            InlineScript inlineScript;
            try {
                inlineScript = createInlineScript(skillDir, pythonCode);
            } catch (IOException ex) {
                return failure(result, "Failed to prepare inline python script: " + safeErrorMessage(ex, "Unknown error"));
            }
            List<String> command = buildInlinePythonCommand(inlineScript.path(), parsedArgs.values);
            target = new ExecutionTarget("inline", inlineScript.path(), command, inlineScript.path(), inlineScript.directory());
        }

        result.put("skill", skill.toLowerCase(Locale.ROOT));
        result.put("scriptSource", target.scriptSource());
        result.put("script", target.scriptPath().toString());
        result.put("timeoutMs", timeoutMs);
        result.put("command", String.join(" ", target.command()));

        try {
            Process process;
            try {
                process = new ProcessBuilder(target.command())
                        .directory(skillDir.toFile())
                        .start();
            } catch (IOException ex) {
                result.put("ok", false);
                result.put("timedOut", false);
                result.put("exitCode", -1);
                result.put("stdout", "");
                result.put("stderr", safeErrorMessage(ex, "Failed to start process"));
                return result;
            }

            StreamCollector stdoutCollector = new StreamCollector(process.getInputStream(), MAX_OUTPUT_CHARS);
            StreamCollector stderrCollector = new StreamCollector(process.getErrorStream(), MAX_OUTPUT_CHARS);
            Thread stdoutThread = new Thread(stdoutCollector, "skill-script-stdout");
            Thread stderrThread = new Thread(stderrCollector, "skill-script-stderr");
            stdoutThread.setDaemon(true);
            stderrThread.setDaemon(true);
            stdoutThread.start();
            stderrThread.start();

            boolean timedOut = false;
            int exitCode = -1;
            try {
                boolean finished = process.waitFor(timeoutMs, TimeUnit.MILLISECONDS);
                if (!finished) {
                    timedOut = true;
                    process.destroyForcibly();
                    process.waitFor(1, TimeUnit.SECONDS);
                }
                if (!timedOut) {
                    exitCode = process.exitValue();
                }
            } catch (InterruptedException ex) {
                Thread.currentThread().interrupt();
                timedOut = true;
                process.destroyForcibly();
            }

            joinQuietly(stdoutThread);
            joinQuietly(stderrThread);

            result.put("ok", !timedOut && exitCode == 0);
            result.put("timedOut", timedOut);
            result.put("exitCode", timedOut ? -1 : exitCode);
            result.put("stdout", stdoutCollector.text());
            result.put("stderr", stderrCollector.text());
            return result;
        } finally {
            cleanupInlineScript(target.cleanupScriptPath(), target.cleanupScriptDir());
        }
    }

    private void joinQuietly(Thread thread) {
        if (thread == null) {
            return;
        }
        try {
            thread.join(500);
        } catch (InterruptedException ex) {
            Thread.currentThread().interrupt();
        }
    }

    private Path resolveScriptPath(Path skillDir, String script) {
        if (script == null || script.isBlank()) {
            return null;
        }
        Path scriptPath = Path.of(script.trim());
        if (scriptPath.isAbsolute()) {
            return null;
        }
        Path resolved = skillDir.resolve(scriptPath).normalize();
        if (!resolved.startsWith(skillDir)) {
            return null;
        }
        return resolved;
    }

    private InlineScript createInlineScript(Path skillDir, String pythonCode) throws IOException {
        String skillTempDirName = safeTempSegment(skillDir.getFileName() == null ? "" : skillDir.getFileName().toString());
        Path scriptDir = INLINE_SCRIPT_ROOT.resolve(skillTempDirName).normalize();
        if (!scriptDir.startsWith(INLINE_SCRIPT_ROOT)) {
            throw new IOException("Illegal inline script temp directory");
        }
        Files.createDirectories(scriptDir);
        Path scriptPath = scriptDir.resolve("inline_" + UUID.randomUUID() + ".py").normalize();
        if (!scriptPath.startsWith(scriptDir)) {
            throw new IOException("Illegal inline script temp path");
        }
        Files.writeString(scriptPath, pythonCode, StandardCharsets.UTF_8);
        return new InlineScript(scriptPath, scriptDir);
    }

    private String safeTempSegment(String text) {
        if (text == null || text.isBlank()) {
            return "unknown";
        }
        return text.replaceAll("[^a-zA-Z0-9._-]", "_");
    }

    private List<String> buildInlinePythonCommand(Path scriptPath, List<String> args) {
        List<String> command = new ArrayList<>();
        command.add(pythonCommand);
        command.add(scriptPath.toString());
        if (args != null && !args.isEmpty()) {
            command.addAll(args);
        }
        return command;
    }

    private List<String> buildCommand(Path scriptPath, List<String> args) {
        String fileName = scriptPath.getFileName() == null ? "" : scriptPath.getFileName().toString().toLowerCase(Locale.ROOT);
        List<String> command = new ArrayList<>();
        if (fileName.endsWith(".py")) {
            command.add(pythonCommand);
        } else if (fileName.endsWith(".sh")) {
            command.add(bashCommand);
        } else {
            return List.of();
        }
        command.add(scriptPath.toString());
        if (args != null && !args.isEmpty()) {
            command.addAll(args);
        }
        return command;
    }

    private int parseTimeoutMs(Object timeoutRaw) {
        if (timeoutRaw instanceof Number number) {
            return clampTimeout(number.intValue());
        }
        if (timeoutRaw instanceof String text && !text.isBlank()) {
            try {
                return clampTimeout(Integer.parseInt(text.trim()));
            } catch (NumberFormatException ignored) {
                return DEFAULT_TIMEOUT_MS;
            }
        }
        return DEFAULT_TIMEOUT_MS;
    }

    private int clampTimeout(int timeoutMs) {
        if (timeoutMs <= 0) {
            return DEFAULT_TIMEOUT_MS;
        }
        return Math.min(timeoutMs, MAX_TIMEOUT_MS);
    }

    private ParseArgsResult parseScriptArgs(Object rawArgs) {
        if (rawArgs == null) {
            return new ParseArgsResult(List.of(), null);
        }
        if (!(rawArgs instanceof List<?> list)) {
            return new ParseArgsResult(List.of(), "Invalid args. Expected array of strings.");
        }
        List<String> parsed = new ArrayList<>();
        for (Object value : list) {
            if (value == null) {
                continue;
            }
            parsed.add(value.toString());
        }
        return new ParseArgsResult(List.copyOf(parsed), null);
    }

    private String readText(Map<String, Object> args, String key) {
        if (args == null || key == null) {
            return null;
        }
        Object value = args.get(key);
        if (value == null) {
            return null;
        }
        String text = value.toString().trim();
        return text.isEmpty() ? null : text;
    }

    private String readCode(Map<String, Object> args, String key) {
        if (args == null || key == null) {
            return null;
        }
        Object value = args.get(key);
        if (value == null) {
            return null;
        }
        String text = value.toString();
        if (text.trim().isEmpty()) {
            return null;
        }
        return text;
    }

    private String safeErrorMessage(Exception ex, String defaultMessage) {
        if (ex == null || ex.getMessage() == null || ex.getMessage().isBlank()) {
            return defaultMessage;
        }
        return ex.getMessage();
    }

    private void cleanupInlineScript(Path scriptPath, Path scriptDir) {
        if (scriptPath == null) {
            return;
        }
        try {
            Files.deleteIfExists(scriptPath);
        } catch (IOException ignored) {
            // best effort cleanup
        }
        if (scriptDir != null) {
            try {
                Files.deleteIfExists(scriptDir);
            } catch (IOException ignored) {
                // best effort cleanup
            }
        }
    }

    private JsonNode failure(ObjectNode result, String error) {
        result.put("ok", false);
        result.put("timedOut", false);
        result.put("exitCode", -1);
        result.put("stdout", "");
        result.put("stderr", "");
        result.put("error", error);
        return result;
    }

    private record ParseArgsResult(List<String> values, String error) {
    }

    private record InlineScript(Path path, Path directory) {
    }

    private record ExecutionTarget(
            String scriptSource,
            Path scriptPath,
            List<String> command,
            Path cleanupScriptPath,
            Path cleanupScriptDir
    ) {
    }

    private static final class StreamCollector implements Runnable {

        private final InputStream stream;
        private final int maxChars;
        private final StringBuilder out = new StringBuilder();
        private boolean truncated;

        private StreamCollector(InputStream stream, int maxChars) {
            this.stream = stream;
            this.maxChars = Math.max(256, maxChars);
        }

        @Override
        public void run() {
            if (stream == null) {
                return;
            }
            try (InputStream input = stream; InputStreamReader reader = new InputStreamReader(input, StandardCharsets.UTF_8)) {
                char[] buffer = new char[1024];
                int len;
                while ((len = reader.read(buffer)) >= 0) {
                    append(buffer, len);
                }
            } catch (IOException ignored) {
                // swallow stream read errors to avoid masking tool execution result.
            }
        }

        private void append(char[] chars, int len) {
            if (len <= 0) {
                return;
            }
            if (out.length() >= maxChars) {
                truncated = true;
                return;
            }
            int remain = maxChars - out.length();
            int toWrite = Math.min(remain, len);
            out.append(chars, 0, toWrite);
            if (toWrite < len) {
                truncated = true;
            }
        }

        private String text() {
            if (!truncated) {
                return out.toString();
            }
            return out + "\n[TRUNCATED]";
        }
    }
}
