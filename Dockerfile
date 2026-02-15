FROM maven:3.9.12-eclipse-temurin-21-alpine AS building

WORKDIR /workspace

COPY settings.xml /usr/share/maven/conf/settings.xml
COPY pom.xml ./
COPY libs ./libs
RUN mvn -B -q install:install-file \
    -Dfile=libs/agw-springai-sdk-0.0.1-SNAPSHOT.jar \
    -DgroupId=com.aiagent \
    -DartifactId=agw-springai-sdk \
    -Dversion=0.0.1-SNAPSHOT \
    -Dpackaging=jar \
 && mvn -B -q -DskipTests dependency:go-offline

COPY src ./src
RUN mvn -B -DskipTests clean package

FROM eclipse-temurin:21-jre-jammy AS running

WORKDIR /opt/app

RUN apt-get update \
 && apt-get install -y --no-install-recommends python3 python3-venv python3-pip bash \
 && python3 -m venv /opt/venv \
 && /opt/venv/bin/pip install --no-cache-dir --upgrade pip \
 && rm -rf /var/lib/apt/lists/*

ENV JAVA_OPTS="-server -Xms256m -XX:MaxRAMPercentage=60" \
    JAVA_AGENT="" \
    PATH="/opt/venv/bin:${PATH}" \
    SPRING_CONFIG_ADDITIONAL_LOCATION="optional:file:/opt/application.yml" \
    AGENT_EXTERNAL_DIR="/opt/agents" \
    AGENT_VIEWPORT_EXTERNAL_DIR="/opt/viewports" \
    AGENT_TOOLS_EXTERNAL_DIR="/opt/tools" \
    AGENT_SKILL_EXTERNAL_DIR="/opt/skills" \
    MEMORY_CHAT_DIR="/opt/chats"

COPY --from=building /workspace/target/springai-agw-0.0.1-SNAPSHOT.jar /opt/app.jar

EXPOSE 8080

ENTRYPOINT ["sh", "-c", "exec java $JAVA_OPTS $JAVA_AGENT -jar /opt/app.jar"]
