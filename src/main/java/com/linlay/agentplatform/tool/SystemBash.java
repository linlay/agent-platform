package com.linlay.agentplatform.tool;

import com.fasterxml.jackson.databind.JsonNode;
import com.linlay.agentplatform.config.BashToolProperties;
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

/* 通过配置可以放开部分目录和命令
```yaml
agent:
  tools:
    bash:
      working-directory: /opt/app
      allowed-paths:
        - /opt
      allowed-commands:
        - ls,pwd,cat,head,tail,top,free,df,git
      path-checked-commands:
        - ls,cat,head,tail,git
```
*/

@Component
public class SystemBash extends AbstractDeterministicTool {

    private static final int MAX_OUTPUT_CHARS = 2000;
    private static final String COMMANDS_NOT_CONFIGURED_MESSAGE = "Bash command whitelist is empty. Configure agent.tools.bash.allowed-commands";
    private final Path workingDirectory;
    private final List<Path> allowedRoots;
    private final Set<String> allowedCommands;
    private final Set<String> pathCheckedCommands;

    @Autowired
    public SystemBash(BashToolProperties properties) {
        this(resolveWorkingDirectory(properties.getWorkingDirectory()),
             parseAllowedPaths(properties.getAllowedPaths()),
             parseCommandSet(properties.getAllowedCommands()),
             parseCommandSet(properties.getPathCheckedCommands()));
    }

    public SystemBash() {
        this(resolveWorkingDirectory(""), List.of(), Set.of(), Set.of());
    }

    SystemBash(Path workingDirectory) {
        this(workingDirectory, List.of(), Set.of(), Set.of());
    }

    SystemBash(Path workingDirectory, List<Path> additionalAllowedRoots) {
        this(workingDirectory, additionalAllowedRoots, Set.of(), Set.of());
    }

    SystemBash(Path workingDirectory, List<Path> additionalAllowedRoots, Set<String> allowedCommands, Set<String> pathCheckedCommands) {
        this.workingDirectory = workingDirectory.toAbsolutePath().normalize();
        this.allowedRoots = buildAllowedRoots(additionalAllowedRoots);
        this.allowedCommands = normalizeCommandSet(allowedCommands);
        this.pathCheckedCommands = resolvePathCheckedCommands(pathCheckedCommands, this.allowedCommands);
    }

    @Override
    public String name() {
        return "_bash_";
    }

    @Override
    public JsonNode invoke(Map<String, Object> args) {
        Map<String, Object> safeArgs = args == null ? Map.of() : args;
        String rawCommand = String.valueOf(safeArgs.getOrDefault("command", "")).trim();

        if (rawCommand.isEmpty()) {
            return textResult(-1, "", "Missing argument: command");
        }

        List<String> tokens = tokenize(rawCommand);
        if (tokens.isEmpty()) {
            return textResult(-1, "", "Cannot parse command");
        }

        if (allowedCommands.isEmpty()) {
            return textResult(-1, "", COMMANDS_NOT_CONFIGURED_MESSAGE);
        }

        String baseCommand = tokens.get(0);
        if (!allowedCommands.contains(baseCommand)) {
            return textResult(-1, "", "Command not allowed: " + baseCommand);
        }

        String argsError = unsafeArgumentError(tokens);
        if (argsError != null) {
            return textResult(-1, "", argsError);
        }

        List<String> expanded = expandPathGlobs(tokens);
        String expandedArgsError = unsafeArgumentError(expanded);
        if (expandedArgsError != null) {
            return textResult(-1, "", expandedArgsError);
        }

        List<String> normalized = normalize(expanded);

        try {
            Process process = new ProcessBuilder(normalized)
                    .directory(workingDirectory.toFile())
                    .start();
            boolean finished = process.waitFor(5, TimeUnit.SECONDS);
            if (!finished) {
                process.destroyForcibly();
                return textResult(-1, "", "Command timed out");
            }

            int exitCode = process.exitValue();
            String stdout = new String(process.getInputStream().readAllBytes(), StandardCharsets.UTF_8);
            String stderr = new String(process.getErrorStream().readAllBytes(), StandardCharsets.UTF_8);
            return textResult(exitCode, truncate(stdout), truncate(stderr));
        } catch (IOException | InterruptedException ex) {
            if (ex instanceof InterruptedException) {
                Thread.currentThread().interrupt();
            }
            String message = ex.getMessage() == null ? "Unknown error" : ex.getMessage();
            return textResult(-1, "", message);
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
        if (!pathCheckedCommands.contains(baseCommand) || tokens.size() == 1) {
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

    private String unsafeArgumentError(List<String> tokens) {
        String baseCommand = tokens.get(0);
        if (!pathCheckedCommands.contains(baseCommand) || tokens.size() == 1) {
            return null;
        }

        for (int i = 1; i < tokens.size(); i++) {
            String token = tokens.get(i);
            if (token.startsWith("-")) {
                continue;
            }
            Path resolved = resolvePath(token);
            if (!isAllowedPath(resolved)) {
                return "Path not allowed outside authorized directories: " + token;
            }
        }
        return null;
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
        for (String entry : configuredAllowedPaths) {
            if (entry == null || entry.isBlank()) {
                continue;
            }
            for (String path : entry.split(",")) {
                String trimmed = path.trim();
                if (!trimmed.isEmpty()) {
                    paths.add(Path.of(trimmed).toAbsolutePath().normalize());
                }
            }
        }
        return paths;
    }

    private static Set<String> parseCommandSet(List<String> configured) {
        LinkedHashSet<String> commands = new LinkedHashSet<>();
        if (configured == null || configured.isEmpty()) {
            return Set.of();
        }
        for (String entry : configured) {
            if (entry == null || entry.isBlank()) {
                continue;
            }
            for (String cmd : entry.split(",")) {
                String trimmed = cmd.trim();
                if (!trimmed.isEmpty()) {
                    commands.add(trimmed);
                }
            }
        }
        return commands.isEmpty() ? Set.of() : Set.copyOf(commands);
    }

    private static Set<String> normalizeCommandSet(Set<String> commands) {
        if (commands == null || commands.isEmpty()) {
            return Set.of();
        }
        LinkedHashSet<String> normalized = new LinkedHashSet<>();
        for (String command : commands) {
            if (command == null || command.isBlank()) {
                continue;
            }
            normalized.add(command.trim());
        }
        return normalized.isEmpty() ? Set.of() : Set.copyOf(normalized);
    }

    private static Set<String> resolvePathCheckedCommands(Set<String> configuredPathCheckedCommands, Set<String> allowedCommands) {
        if (allowedCommands.isEmpty()) {
            return Set.of();
        }
        Set<String> normalized = normalizeCommandSet(configuredPathCheckedCommands);
        if (normalized.isEmpty()) {
            return allowedCommands;
        }
        LinkedHashSet<String> intersected = new LinkedHashSet<>();
        for (String command : normalized) {
            if (allowedCommands.contains(command)) {
                intersected.add(command);
            }
        }
        return intersected.isEmpty() ? Set.of() : Set.copyOf(intersected);
    }

    private static List<Path> buildAllowedRoots(List<Path> additionalAllowedRoots) {
        LinkedHashSet<Path> roots = new LinkedHashSet<>();
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

    private JsonNode textResult(int exitCode, String stdout, String stderr) {
        String safeStdout = stdout == null ? "" : stdout;
        String safeStderr = stderr == null ? "" : stderr;
        String text = "exitCode: " + exitCode
                + "\n\"workingDirectory\": \"" + workingDirectory + "\""
                + "\nstdout:\n" + safeStdout
                + "\nstderr:\n" + safeStderr;
        return OBJECT_MAPPER.getNodeFactory().textNode(text);
    }
}
