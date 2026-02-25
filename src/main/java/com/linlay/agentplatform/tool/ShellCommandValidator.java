package com.linlay.agentplatform.tool;

import java.nio.file.InvalidPathException;
import java.nio.file.Path;
import java.util.ArrayDeque;
import java.util.ArrayList;
import java.util.Deque;
import java.util.List;
import java.util.Locale;
import java.util.Set;

final class ShellCommandValidator {

    private static final int MAX_SUBSTITUTION_DEPTH = 8;

    private static final Set<String> STRUCTURAL_KEYWORDS = Set.of(
            "if", "then", "else", "elif", "fi",
            "do", "done", "while", "until", "for",
            "case", "in", "esac", "select", "function"
    );

    private static final Set<String> UNSUPPORTED_COMMANDS = Set.of(
            ".", "source", "eval", "exec", "coproc", "fg", "bg", "jobs"
    );

    private final Path workingDirectory;
    private final List<Path> allowedRoots;
    private final Set<String> allowedCommands;
    private final Set<String> pathCheckedCommands;

    ShellCommandValidator(Path workingDirectory,
                          List<Path> allowedRoots,
                          Set<String> allowedCommands,
                          Set<String> pathCheckedCommands) {
        this.workingDirectory = workingDirectory;
        this.allowedRoots = allowedRoots;
        this.allowedCommands = allowedCommands;
        this.pathCheckedCommands = pathCheckedCommands;
    }

    String validate(String rawCommand) {
        return validateRecursive(rawCommand, 0);
    }

    private String validateRecursive(String rawCommand, int depth) {
        if (depth > MAX_SUBSTITUTION_DEPTH) {
            return "Unsupported syntax for _bash_: command substitution nesting is too deep";
        }

        StripHereDocResult stripResult = stripHereDocBodies(rawCommand);
        if (stripResult.error != null) {
            return stripResult.error;
        }

        String script = stripResult.script;
        String syntaxError = unsupportedSyntaxError(script);
        if (syntaxError != null) {
            return syntaxError;
        }

        String substitutionError = validateCommandSubstitutions(script, depth);
        if (substitutionError != null) {
            return substitutionError;
        }

        TokenizeResult tokenizeResult = tokenize(script);
        if (tokenizeResult.error != null) {
            return tokenizeResult.error;
        }
        if (tokenizeResult.tokens.isEmpty()) {
            return "Cannot parse command";
        }

        return validateTokenStream(tokenizeResult.tokens);
    }

    private String unsupportedSyntaxError(String script) {
        boolean singleQuoted = false;
        boolean doubleQuoted = false;
        boolean escaped = false;

        for (int i = 0; i < script.length(); i++) {
            char ch = script.charAt(i);
            if (escaped) {
                escaped = false;
                continue;
            }
            if (singleQuoted) {
                if (ch == '\'') {
                    singleQuoted = false;
                }
                continue;
            }
            if (doubleQuoted) {
                if (ch == '"') {
                    doubleQuoted = false;
                } else if (ch == '\\') {
                    escaped = true;
                }
                continue;
            }

            if (ch == '\\') {
                escaped = true;
                continue;
            }
            if (ch == '\'') {
                singleQuoted = true;
                continue;
            }
            if (ch == '"') {
                doubleQuoted = true;
                continue;
            }
            if (ch == '`') {
                return "Unsupported syntax for _bash_: backtick command substitution";
            }
            if (ch == '<' && i + 1 < script.length() && script.charAt(i + 1) == '(') {
                return "Unsupported syntax for _bash_: process substitution";
            }
            if (ch == '>' && i + 1 < script.length() && script.charAt(i + 1) == '(') {
                return "Unsupported syntax for _bash_: process substitution";
            }
        }
        return null;
    }

    private String validateCommandSubstitutions(String script, int depth) {
        boolean singleQuoted = false;
        boolean doubleQuoted = false;
        boolean escaped = false;

        for (int i = 0; i < script.length(); i++) {
            char ch = script.charAt(i);
            if (escaped) {
                escaped = false;
                continue;
            }
            if (singleQuoted) {
                if (ch == '\'') {
                    singleQuoted = false;
                }
                continue;
            }
            if (doubleQuoted) {
                if (ch == '"') {
                    doubleQuoted = false;
                } else if (ch == '\\') {
                    escaped = true;
                }
            } else {
                if (ch == '\\') {
                    escaped = true;
                    continue;
                }
                if (ch == '\'') {
                    singleQuoted = true;
                    continue;
                }
                if (ch == '"') {
                    doubleQuoted = true;
                    continue;
                }
            }

            if (!singleQuoted && ch == '$' && i + 1 < script.length() && script.charAt(i + 1) == '(') {
                if (i + 2 < script.length() && script.charAt(i + 2) == '(') {
                    int arithmeticEnd = findArithmeticSubstitutionEnd(script, i + 3);
                    if (arithmeticEnd < 0) {
                        return "Cannot parse command: unterminated arithmetic substitution";
                    }
                    i = arithmeticEnd;
                    continue;
                }

                int end = findCommandSubstitutionEnd(script, i + 2);
                if (end < 0) {
                    return "Cannot parse command: unterminated command substitution";
                }

                String inner = script.substring(i + 2, end);
                String innerError = validateRecursive(inner, depth + 1);
                if (innerError != null) {
                    return innerError;
                }
                i = end;
            }
        }

        return null;
    }

    private int findArithmeticSubstitutionEnd(String script, int start) {
        int depth = 1;
        for (int i = start; i < script.length() - 1; i++) {
            char ch = script.charAt(i);
            char next = script.charAt(i + 1);
            if (ch == '(' && next == '(') {
                depth++;
                i++;
                continue;
            }
            if (ch == ')' && next == ')') {
                depth--;
                if (depth == 0) {
                    return i + 1;
                }
                i++;
            }
        }
        return -1;
    }

    private int findCommandSubstitutionEnd(String script, int start) {
        int depth = 1;
        boolean singleQuoted = false;
        boolean doubleQuoted = false;
        boolean escaped = false;

        for (int i = start; i < script.length(); i++) {
            char ch = script.charAt(i);
            if (escaped) {
                escaped = false;
                continue;
            }
            if (singleQuoted) {
                if (ch == '\'') {
                    singleQuoted = false;
                }
                continue;
            }
            if (doubleQuoted) {
                if (ch == '"') {
                    doubleQuoted = false;
                } else if (ch == '\\') {
                    escaped = true;
                }
                continue;
            }

            if (ch == '\\') {
                escaped = true;
                continue;
            }
            if (ch == '\'') {
                singleQuoted = true;
                continue;
            }
            if (ch == '"') {
                doubleQuoted = true;
                continue;
            }
            if (ch == '$' && i + 1 < script.length() && script.charAt(i + 1) == '(') {
                depth++;
                i++;
                continue;
            }
            if (ch == ')') {
                depth--;
                if (depth == 0) {
                    return i;
                }
            }
        }

        return -1;
    }

    private StripHereDocResult stripHereDocBodies(String script) {
        String[] lines = script.split("\\n", -1);
        StringBuilder out = new StringBuilder();
        Deque<HereDocSpec> pending = new ArrayDeque<>();

        for (int i = 0; i < lines.length; i++) {
            String line = lines[i];
            if (!pending.isEmpty()) {
                HereDocSpec spec = pending.peek();
                String compare = spec.stripTabs ? stripLeadingTabs(line) : line;
                if (compare.equals(spec.delimiter)) {
                    pending.poll();
                }
                continue;
            }

            DetectHereDocResult detect = detectHereDocSpecs(line);
            if (detect.error != null) {
                return new StripHereDocResult("", detect.error);
            }
            pending.addAll(detect.specs);

            out.append(line);
            if (i < lines.length - 1) {
                out.append('\n');
            }
        }

        if (!pending.isEmpty()) {
            return new StripHereDocResult("", "Cannot parse command: unterminated here-doc");
        }

        return new StripHereDocResult(out.toString(), null);
    }

    private DetectHereDocResult detectHereDocSpecs(String line) {
        List<HereDocSpec> specs = new ArrayList<>();
        boolean singleQuoted = false;
        boolean doubleQuoted = false;
        boolean escaped = false;

        for (int i = 0; i < line.length(); i++) {
            char ch = line.charAt(i);
            if (escaped) {
                escaped = false;
                continue;
            }
            if (singleQuoted) {
                if (ch == '\'') {
                    singleQuoted = false;
                }
                continue;
            }
            if (doubleQuoted) {
                if (ch == '"') {
                    doubleQuoted = false;
                } else if (ch == '\\') {
                    escaped = true;
                }
                continue;
            }

            if (ch == '\\') {
                escaped = true;
                continue;
            }
            if (ch == '\'') {
                singleQuoted = true;
                continue;
            }
            if (ch == '"') {
                doubleQuoted = true;
                continue;
            }

            if (ch == '#') {
                break;
            }

            if (ch == '<' && i + 1 < line.length() && line.charAt(i + 1) == '<') {
                int cursor = i + 2;
                boolean stripTabs = false;
                if (cursor < line.length() && line.charAt(cursor) == '-') {
                    stripTabs = true;
                    cursor++;
                }

                while (cursor < line.length() && (line.charAt(cursor) == ' ' || line.charAt(cursor) == '\t')) {
                    cursor++;
                }

                if (cursor >= line.length()) {
                    return new DetectHereDocResult(List.of(), "Cannot parse command: missing here-doc delimiter");
                }

                String delimiter;
                char delimiterStart = line.charAt(cursor);
                if (delimiterStart == '\'' || delimiterStart == '"') {
                    char quote = delimiterStart;
                    cursor++;
                    int start = cursor;
                    while (cursor < line.length() && line.charAt(cursor) != quote) {
                        cursor++;
                    }
                    if (cursor >= line.length()) {
                        return new DetectHereDocResult(List.of(), "Cannot parse command: unterminated quoted here-doc delimiter");
                    }
                    delimiter = line.substring(start, cursor);
                    cursor++;
                } else {
                    int start = cursor;
                    while (cursor < line.length() && !Character.isWhitespace(line.charAt(cursor)) && !isOperatorChar(line.charAt(cursor))) {
                        cursor++;
                    }
                    delimiter = line.substring(start, cursor);
                }

                if (delimiter.isBlank()) {
                    return new DetectHereDocResult(List.of(), "Cannot parse command: missing here-doc delimiter");
                }

                specs.add(new HereDocSpec(delimiter, stripTabs));
                i = Math.max(i, cursor - 1);
            }
        }

        return new DetectHereDocResult(specs, null);
    }

    private String stripLeadingTabs(String line) {
        int index = 0;
        while (index < line.length() && line.charAt(index) == '\t') {
            index++;
        }
        return line.substring(index);
    }

    private boolean isOperatorChar(char ch) {
        return ch == ';' || ch == '|' || ch == '&' || ch == '(' || ch == ')' || ch == '{' || ch == '}' || ch == '<' || ch == '>';
    }

    private TokenizeResult tokenize(String script) {
        List<Token> tokens = new ArrayList<>();
        StringBuilder word = new StringBuilder();
        boolean singleQuoted = false;
        boolean doubleQuoted = false;
        boolean escaped = false;

        for (int i = 0; i < script.length(); i++) {
            char ch = script.charAt(i);
            if (escaped) {
                word.append(ch);
                escaped = false;
                continue;
            }
            if (singleQuoted) {
                word.append(ch);
                if (ch == '\'') {
                    singleQuoted = false;
                }
                continue;
            }
            if (doubleQuoted) {
                word.append(ch);
                if (ch == '"') {
                    doubleQuoted = false;
                } else if (ch == '\\') {
                    escaped = true;
                }
                continue;
            }

            if (ch == '\\') {
                word.append(ch);
                escaped = true;
                continue;
            }
            if (ch == '\'') {
                word.append(ch);
                singleQuoted = true;
                continue;
            }
            if (ch == '"') {
                word.append(ch);
                doubleQuoted = true;
                continue;
            }
            if (ch == '#' && word.length() == 0) {
                while (i < script.length() && script.charAt(i) != '\n') {
                    i++;
                }
                if (i < script.length() && script.charAt(i) == '\n') {
                    tokens.add(Token.newline());
                }
                continue;
            }
            if (Character.isWhitespace(ch)) {
                flushWord(tokens, word);
                if (ch == '\n') {
                    tokens.add(Token.newline());
                }
                continue;
            }

            String op = matchOperator(script, i);
            if (op != null) {
                flushWord(tokens, word);
                tokens.add(Token.op(op));
                i += op.length() - 1;
                continue;
            }

            word.append(ch);
        }

        if (singleQuoted || doubleQuoted) {
            return new TokenizeResult(List.of(), "Cannot parse command: unterminated quote");
        }

        flushWord(tokens, word);
        return new TokenizeResult(tokens, null);
    }

    private void flushWord(List<Token> tokens, StringBuilder word) {
        if (word.length() == 0) {
            return;
        }
        tokens.add(Token.word(word.toString()));
        word.setLength(0);
    }

    private String matchOperator(String script, int index) {
        String[] multiCharOps = {"|&", "||", "&&", "<<-", "<<<", "<<", ">>", "2>&1", ";;&", ";&", ";;", "&>"};
        for (String op : multiCharOps) {
            if (script.startsWith(op, index)) {
                return op;
            }
        }

        char ch = script.charAt(index);
        if (Character.isDigit(ch)) {
            int cursor = index;
            while (cursor < script.length() && Character.isDigit(script.charAt(cursor))) {
                cursor++;
            }
            if (cursor < script.length()) {
                if (script.startsWith(">>", cursor)) {
                    return script.substring(index, cursor + 2);
                }
                char redir = script.charAt(cursor);
                if (redir == '>' || redir == '<') {
                    return script.substring(index, cursor + 1);
                }
            }
        }

        if (ch == '|' || ch == '&' || ch == ';' || ch == '(' || ch == ')' || ch == '{' || ch == '}' || ch == '>' || ch == '<') {
            return String.valueOf(ch);
        }

        return null;
    }

    private String validateTokenStream(List<Token> tokens) {
        ParseContext context = ParseContext.NORMAL;
        int caseDepth = 0;

        int index = 0;
        while (index < tokens.size()) {
            Token token = tokens.get(index);
            if (token.isSeparator()) {
                if ("&".equals(token.text)) {
                    return "Unsupported syntax for _bash_: background/job control";
                }
                if (context == ParseContext.CASE_PATTERN && ")".equals(token.text)) {
                    context = ParseContext.NORMAL;
                }
                if (context == ParseContext.NORMAL && caseDepth > 0 && isCaseArmSeparator(token.text)) {
                    context = ParseContext.CASE_PATTERN;
                }
                index++;
                continue;
            }

            if (!token.isWord()) {
                index++;
                continue;
            }

            String word = normalizeWord(token.text);
            if (word.isEmpty()) {
                index++;
                continue;
            }
            String lower = word.toLowerCase(Locale.ROOT);

            if (context == ParseContext.FOR_HEADER) {
                if ("do".equals(lower)) {
                    context = ParseContext.NORMAL;
                }
                index++;
                continue;
            }

            if (context == ParseContext.CASE_HEADER) {
                if ("in".equals(lower)) {
                    context = ParseContext.CASE_PATTERN;
                }
                index++;
                continue;
            }

            if (context == ParseContext.CASE_PATTERN) {
                index++;
                continue;
            }

            if (STRUCTURAL_KEYWORDS.contains(lower)) {
                if ("for".equals(lower)) {
                    context = ParseContext.FOR_HEADER;
                } else if ("case".equals(lower)) {
                    caseDepth++;
                    context = ParseContext.CASE_HEADER;
                } else if ("esac".equals(lower)) {
                    if (caseDepth > 0) {
                        caseDepth--;
                    }
                    context = caseDepth > 0 ? ParseContext.CASE_PATTERN : ParseContext.NORMAL;
                }
                index++;
                continue;
            }

            ParseCommandResult commandResult = parseSimpleCommand(tokens, index);
            if (commandResult.error != null) {
                return commandResult.error;
            }
            if (commandResult.command == null) {
                index = commandResult.nextIndex;
                continue;
            }

            CommandSpec command = commandResult.command;
            if (UNSUPPORTED_COMMANDS.contains(command.commandName)) {
                return "Unsupported syntax for _bash_: " + command.commandName;
            }
            if (!allowedCommands.contains(command.commandName)) {
                return "Command not allowed: " + command.commandName;
            }

            if (pathCheckedCommands.contains(command.commandName)) {
                String pathError = validatePathCheckedCommand(command);
                if (pathError != null) {
                    return pathError;
                }
            }

            index = commandResult.nextIndex;
        }

        return null;
    }

    private ParseCommandResult parseSimpleCommand(List<Token> tokens, int startIndex) {
        List<String> words = new ArrayList<>();
        List<String> redirectionTargets = new ArrayList<>();

        int index = startIndex;
        while (index < tokens.size()) {
            Token token = tokens.get(index);
            if (token.isSeparator()) {
                break;
            }

            if (token.isOperator() && isRedirectionOperator(token.text)) {
                if ("2>&1".equals(token.text)) {
                    index++;
                    continue;
                }

                if (index + 1 >= tokens.size() || !tokens.get(index + 1).isWord()) {
                    return new ParseCommandResult(index, null, "Cannot parse command: malformed redirection");
                }

                String target = normalizeWord(tokens.get(index + 1).text);
                if (target.isEmpty()) {
                    return new ParseCommandResult(index, null, "Cannot parse command: malformed redirection target");
                }

                if (!isHereDocOperator(token.text) && !"<<<".equals(token.text)) {
                    redirectionTargets.add(target);
                }

                index += 2;
                continue;
            }

            if (token.isWord()) {
                words.add(normalizeWord(token.text));
                index++;
                continue;
            }

            index++;
        }

        int commandIndex = 0;
        while (commandIndex < words.size() && isAssignment(words.get(commandIndex))) {
            commandIndex++;
        }

        if (commandIndex >= words.size()) {
            return new ParseCommandResult(index, null, null);
        }

        String commandName = words.get(commandIndex);
        if (commandName.isBlank()) {
            return new ParseCommandResult(index, null, null);
        }
        if (containsDynamicCommandSyntax(commandName)) {
            return new ParseCommandResult(index, null, "Unsupported syntax for _bash_: dynamic command name " + commandName);
        }

        List<String> args = new ArrayList<>();
        for (int i = commandIndex + 1; i < words.size(); i++) {
            args.add(words.get(i));
        }

        return new ParseCommandResult(index, new CommandSpec(commandName, args, redirectionTargets), null);
    }

    private String validatePathCheckedCommand(CommandSpec command) {
        for (String arg : command.args) {
            if (arg == null || arg.isBlank() || arg.startsWith("-")) {
                continue;
            }
            String error = validatePathToken(arg);
            if (error != null) {
                return error;
            }
        }

        for (String redirectionTarget : command.redirectionTargets) {
            String error = validatePathToken(redirectionTarget);
            if (error != null) {
                return error;
            }
        }

        return null;
    }

    private String validatePathToken(String token) {
        if (token.contains("$") || token.contains("`")) {
            return "Path not allowed outside authorized directories: " + token;
        }

        if (containsGlob(token)) {
            Path globRoot = resolveGlobRoot(token);
            if (globRoot == null) {
                return "Path not allowed outside authorized directories: " + token;
            }
            if (!isAllowedPath(globRoot)) {
                return "Path not allowed outside authorized directories: " + token;
            }
            return null;
        }

        Path path = resolvePath(token);
        if (path == null || !isAllowedPath(path)) {
            return "Path not allowed outside authorized directories: " + token;
        }

        return null;
    }

    private Path resolvePath(String token) {
        Path tokenPath;
        try {
            tokenPath = Path.of(token);
        } catch (InvalidPathException ex) {
            return null;
        }

        if (tokenPath.isAbsolute()) {
            return tokenPath.normalize();
        }

        return workingDirectory.resolve(tokenPath).normalize();
    }

    private Path resolveGlobRoot(String token) {
        Path tokenPath;
        try {
            tokenPath = Path.of(token);
        } catch (InvalidPathException ex) {
            return null;
        }

        Path parent = tokenPath.getParent();
        if (parent == null) {
            return workingDirectory;
        }
        if (containsGlob(parent.toString())) {
            return null;
        }
        if (parent.isAbsolute()) {
            return parent.normalize();
        }
        return workingDirectory.resolve(parent).normalize();
    }

    private boolean isAllowedPath(Path path) {
        for (Path allowedRoot : allowedRoots) {
            if (path.startsWith(allowedRoot)) {
                return true;
            }
        }
        return false;
    }

    private boolean containsDynamicCommandSyntax(String commandName) {
        return commandName.contains("$") || commandName.contains("`") || commandName.contains("$(") || commandName.contains("${");
    }

    private boolean isAssignment(String value) {
        if (value == null || value.isBlank()) {
            return false;
        }
        int equals = value.indexOf('=');
        if (equals <= 0) {
            return false;
        }
        String name = value.substring(0, equals);
        if (!Character.isLetter(name.charAt(0)) && name.charAt(0) != '_') {
            return false;
        }
        for (int i = 1; i < name.length(); i++) {
            char ch = name.charAt(i);
            if (!Character.isLetterOrDigit(ch) && ch != '_') {
                return false;
            }
        }
        return true;
    }

    private boolean isCaseArmSeparator(String token) {
        return ";;".equals(token) || ";&".equals(token) || ";;&".equals(token);
    }

    private boolean isHereDocOperator(String operator) {
        return "<<".equals(operator) || "<<-".equals(operator);
    }

    private boolean isRedirectionOperator(String operator) {
        if (operator == null || operator.isBlank()) {
            return false;
        }
        if ("<".equals(operator) || ">".equals(operator) || ">>".equals(operator)
                || "<<".equals(operator) || "<<-".equals(operator)
                || "<<<".equals(operator) || "&>".equals(operator)
                || "2>&1".equals(operator)) {
            return true;
        }
        return operator.matches("\\d+(>|>>|<)");
    }

    private boolean containsGlob(String token) {
        return token.contains("*") || token.contains("?") || token.contains("[");
    }

    private String normalizeWord(String word) {
        if (word == null) {
            return "";
        }
        String result = word.trim();
        while (result.length() >= 2) {
            if ((result.startsWith("\"") && result.endsWith("\""))
                    || (result.startsWith("'") && result.endsWith("'"))) {
                result = result.substring(1, result.length() - 1);
                continue;
            }
            break;
        }
        return result;
    }

    private enum TokenType {
        WORD,
        OP,
        NEWLINE
    }

    private enum ParseContext {
        NORMAL,
        FOR_HEADER,
        CASE_HEADER,
        CASE_PATTERN
    }

    private record Token(TokenType type, String text) {

        private static Token word(String text) {
            return new Token(TokenType.WORD, text);
        }

        private static Token op(String text) {
            return new Token(TokenType.OP, text);
        }

        private static Token newline() {
            return new Token(TokenType.NEWLINE, "\\n");
        }

        private boolean isWord() {
            return type == TokenType.WORD;
        }

        private boolean isOperator() {
            return type == TokenType.OP;
        }

        private boolean isSeparator() {
            if (type == TokenType.NEWLINE) {
                return true;
            }
            if (type != TokenType.OP) {
                return false;
            }
            return "|".equals(text)
                    || "|&".equals(text)
                    || "||".equals(text)
                    || "&&".equals(text)
                    || ";".equals(text)
                    || ";;".equals(text)
                    || ";&".equals(text)
                    || ";;&".equals(text)
                    || "&".equals(text)
                    || "(".equals(text)
                    || ")".equals(text)
                    || "{".equals(text)
                    || "}".equals(text);
        }
    }

    private record StripHereDocResult(String script, String error) {
    }

    private record HereDocSpec(String delimiter, boolean stripTabs) {
    }

    private record DetectHereDocResult(List<HereDocSpec> specs, String error) {
    }

    private record TokenizeResult(List<Token> tokens, String error) {
    }

    private record ParseCommandResult(int nextIndex, CommandSpec command, String error) {
    }

    private record CommandSpec(String commandName, List<String> args, List<String> redirectionTargets) {
    }
}
