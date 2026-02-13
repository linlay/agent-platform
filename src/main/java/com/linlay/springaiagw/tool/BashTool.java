package com.linlay.springaiagw.tool;

import com.fasterxml.jackson.databind.JsonNode;
import com.fasterxml.jackson.databind.node.ObjectNode;
import com.linlay.springaiagw.config.BashToolProperties;
import org.springframework.beans.factory.annotation.Autowired;
import org.springframework.stereotype.Component;

import java.io.IOException;
import java.nio.charset.StandardCharsets;
import java.nio.file.DirectoryStream;
import java.nio.file.Files;
import java.nio.file.Path;
import java.util.ArrayList;
import java.util.Comparator;
import java.util.LinkedHashSet;
import java.util.List;
import java.util.Locale;
import java.util.Map;
import java.util.Set;
import java.util.concurrent.TimeUnit;

/* 通过配置可以放开部分目录
```yaml
agent:
  tools:
    bash:
      working-directory: /opt/app
      allowed-paths:
        - /opt
```
*/

@Component
public class BashTool extends AbstractDeterministicTool {

    private static final Set<String> ALLOWED_COMMANDS = Set.of("ls", "pwd", "cat", "head", "tail", "top", "free", "df");
    private static final Set<String> COMMANDS_WITH_PATH_ARGS = Set.of("ls", "cat", "head", "tail");
    private static final int MAX_OUTPUT_CHARS = 2000;
    private final Path workingDirectory;
    private final List<Path> allowedRoots;

    @Autowired
    public BashTool(BashToolProperties properties) {
        this(resolveWorkingDirectory(properties.getWorkingDirectory()), parseAllowedPaths(properties.getAllowedPaths()));
    }

    public BashTool() {
        this(resolveWorkingDirectory(""), List.of());
    }

    BashTool(Path workingDirectory) {
        this(workingDirectory, List.of());
    }

    BashTool(Path workingDirectory, List<Path> additionalAllowedRoots) {
        this.workingDirectory = workingDirectory.toAbsolutePath().normalize();
        this.allowedRoots = buildAllowedRoots(this.workingDirectory, additionalAllowedRoots);
    }

    @Override
    public String name() {
        return "bash";
    }

    @Override
    public JsonNode invoke(Map<String, Object> args) {
        String rawCommand = String.valueOf(args.getOrDefault("command", "")).trim();

        ObjectNode root = OBJECT_MAPPER.createObjectNode();
        root.put("tool", name());
        root.put("command", rawCommand);
        root.put("allowedCommands", String.join(",", ALLOWED_COMMANDS));

        if (rawCommand.isEmpty()) {
            root.put("ok", false);
            root.put("error", "Missing argument: command");
            return root;
        }

        List<String> tokens = tokenize(rawCommand);
        if (tokens.isEmpty()) {
            root.put("ok", false);
            root.put("error", "Cannot parse command");
            return root;
        }

        String baseCommand = tokens.get(0);
        if (!ALLOWED_COMMANDS.contains(baseCommand)) {
            root.put("ok", false);
            root.put("error", "Command not allowed: " + baseCommand);
            return root;
        }

        if (!hasSafeArguments(tokens, root)) {
            return root;
        }

        List<String> expanded = expandPathGlobs(tokens);
        if (!hasSafeArguments(expanded, root)) {
            return root;
        }

        List<String> normalized = normalize(expanded);
        root.put("normalizedCommand", String.join(" ", normalized));
        root.put("workingDirectory", workingDirectory.toString());
        root.put("allowedRoots", allowedRoots.stream().map(Path::toString).toList().toString());

        try {
            Process process = new ProcessBuilder(normalized)
                    .directory(workingDirectory.toFile())
                    .start();
            boolean finished = process.waitFor(5, TimeUnit.SECONDS);
            if (!finished) {
                process.destroyForcibly();
                root.put("ok", false);
                root.put("timedOut", true);
                root.put("exitCode", -1);
                root.put("stdout", "");
                root.put("stderr", "Command timed out");
                return root;
            }

            int exitCode = process.exitValue();
            String stdout = new String(process.getInputStream().readAllBytes(), StandardCharsets.UTF_8);
            String stderr = new String(process.getErrorStream().readAllBytes(), StandardCharsets.UTF_8);

            root.put("ok", exitCode == 0);
            root.put("timedOut", false);
            root.put("exitCode", exitCode);
            root.put("stdout", truncate(stdout));
            root.put("stderr", truncate(stderr));
            return root;
        } catch (IOException | InterruptedException ex) {
            if (ex instanceof InterruptedException) {
                Thread.currentThread().interrupt();
            }
            root.put("ok", false);
            root.put("timedOut", false);
            root.put("exitCode", -1);
            root.put("stdout", "");
            root.put("stderr", ex.getMessage());
            return root;
        }
    }

    private List<String> tokenize(String rawCommand) {
        String[] split = rawCommand.trim().split("\\s+");
        List<String> tokens = new ArrayList<>();
        for (String token : split) {
            if (!token.isBlank()) {
                tokens.add(token);
            }
        }
        return tokens;
    }

    private List<String> normalize(List<String> tokens) {
        String baseCommand = tokens.get(0);
        if ("top".equals(baseCommand) && tokens.size() == 1) {
            if (isMac()) {
                return List.of("top", "-l", "1");
            }
            return List.of("top", "-b", "-n", "1");
        }
        if ("free".equals(baseCommand) && tokens.size() == 1 && isMac()) {
            return List.of("vm_stat");
        }
        return tokens;
    }

    private List<String> expandPathGlobs(List<String> tokens) {
        String baseCommand = tokens.get(0);
        if (!COMMANDS_WITH_PATH_ARGS.contains(baseCommand) || tokens.size() == 1) {
            return tokens;
        }

        List<String> expanded = new ArrayList<>();
        expanded.add(baseCommand);
        for (int i = 1; i < tokens.size(); i++) {
            String token = tokens.get(i);
            if (token.startsWith("-")) {
                expanded.add(token);
                continue;
            }

            if (!containsGlob(token)) {
                expanded.add(token);
                continue;
            }

            List<String> matches = expandSingleGlobToken(token);
            if (matches.isEmpty()) {
                expanded.add(token);
            } else {
                expanded.addAll(matches);
            }
        }
        return expanded;
    }

    private List<String> expandSingleGlobToken(String token) {
        Path tokenPath = Path.of(token);
        Path parent = tokenPath.getParent();
        String pattern = tokenPath.getFileName() == null ? token : tokenPath.getFileName().toString();

        if (parent != null && containsGlob(parent.toString())) {
            return List.of();
        }

        Path searchDir;
        if (parent == null) {
            searchDir = workingDirectory;
        } else if (parent.isAbsolute()) {
            searchDir = parent.normalize();
        } else {
            searchDir = workingDirectory.resolve(parent).normalize();
        }
        if (!isAllowedPath(searchDir) || !Files.isDirectory(searchDir)) {
            return List.of();
        }

        List<String> matches = new ArrayList<>();
        try (DirectoryStream<Path> stream = Files.newDirectoryStream(searchDir, pattern)) {
            for (Path path : stream) {
                matches.add(relativizeForOutput(path.normalize()));
            }
        } catch (IOException ignored) {
            return List.of();
        }

        matches.sort(Comparator.naturalOrder());
        return matches;
    }

    private boolean containsGlob(String token) {
        return token.contains("*") || token.contains("?") || token.contains("[");
    }

    private boolean hasSafeArguments(List<String> tokens, ObjectNode root) {
        String baseCommand = tokens.get(0);
        if (!COMMANDS_WITH_PATH_ARGS.contains(baseCommand) || tokens.size() == 1) {
            return true;
        }

        for (int i = 1; i < tokens.size(); i++) {
            String token = tokens.get(i);
            if (token.startsWith("-")) {
                continue;
            }
            Path resolved = resolvePath(token);
            if (!isAllowedPath(resolved)) {
                root.put("ok", false);
                root.put("error", "Path not allowed outside authorized directories: " + token);
                return false;
            }
        }
        return true;
    }

    private Path resolvePath(String token) {
        Path tokenPath = Path.of(token);
        if (tokenPath.isAbsolute()) {
            return tokenPath.normalize();
        }
        return workingDirectory.resolve(tokenPath).normalize();
    }

    private boolean isAllowedPath(Path path) {
        for (Path allowedRoot : allowedRoots) {
            if (path.startsWith(allowedRoot)) {
                return true;
            }
        }
        return false;
    }

    private String relativizeForOutput(Path path) {
        if (path.startsWith(workingDirectory)) {
            return workingDirectory.relativize(path).toString();
        }
        return path.toString();
    }

    private static Path resolveWorkingDirectory(String configuredWorkingDirectory) {
        if (configuredWorkingDirectory == null || configuredWorkingDirectory.isBlank()) {
            return Path.of(System.getProperty("user.dir", ".")).toAbsolutePath().normalize();
        }
        return Path.of(configuredWorkingDirectory).toAbsolutePath().normalize();
    }

    private static List<Path> parseAllowedPaths(List<String> configuredAllowedPaths) {
        if (configuredAllowedPaths == null || configuredAllowedPaths.isEmpty()) {
            return List.of();
        }

        List<Path> paths = new ArrayList<>();
        for (String configuredAllowedPath : configuredAllowedPaths) {
            if (configuredAllowedPath == null || configuredAllowedPath.isBlank()) {
                continue;
            }
            paths.add(Path.of(configuredAllowedPath).toAbsolutePath().normalize());
        }
        return paths;
    }

    private static List<Path> buildAllowedRoots(Path workingDirectory, List<Path> additionalAllowedRoots) {
        LinkedHashSet<Path> roots = new LinkedHashSet<>();
        roots.add(workingDirectory.toAbsolutePath().normalize());
        if (additionalAllowedRoots != null) {
            for (Path path : additionalAllowedRoots) {
                if (path != null) {
                    roots.add(path.toAbsolutePath().normalize());
                }
            }
        }
        return List.copyOf(roots);
    }

    private boolean isMac() {
        String osName = System.getProperty("os.name", "").toLowerCase(Locale.ROOT);
        return osName.contains("mac");
    }

    private String truncate(String text) {
        if (text == null || text.isEmpty()) {
            return "";
        }
        if (text.length() <= MAX_OUTPUT_CHARS) {
            return text;
        }
        return text.substring(0, MAX_OUTPUT_CHARS) + "...(truncated)";
    }
}
