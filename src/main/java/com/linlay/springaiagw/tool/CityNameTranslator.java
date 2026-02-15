package com.linlay.springaiagw.tool;

import java.util.Map;
import java.util.Locale;

final class CityNameTranslator {

    private static final Map<String, String> CITY_MAP = Map.ofEntries(
            Map.entry("shanghai", "上海"),
            Map.entry("beijing", "北京"),
            Map.entry("guangzhou", "广州"),
            Map.entry("shenzhen", "深圳"),
            Map.entry("hangzhou", "杭州"),
            Map.entry("chengdu", "成都"),
            Map.entry("wuhan", "武汉"),
            Map.entry("nanjing", "南京"),
            Map.entry("xian", "西安"),
            Map.entry("xi'an", "西安"),
            Map.entry("chongqing", "重庆"),
            Map.entry("tianjin", "天津"),
            Map.entry("suzhou", "苏州"),
            Map.entry("hong kong", "香港"),
            Map.entry("taipei", "台北"),
            Map.entry("singapore", "新加坡"),
            Map.entry("tokyo", "东京"),
            Map.entry("osaka", "大阪"),
            Map.entry("seoul", "首尔"),
            Map.entry("new york", "纽约"),
            Map.entry("los angeles", "洛杉矶"),
            Map.entry("san francisco", "旧金山"),
            Map.entry("london", "伦敦"),
            Map.entry("paris", "巴黎"),
            Map.entry("berlin", "柏林"),
            Map.entry("sydney", "悉尼"),
            Map.entry("melbourne", "墨尔本"),
            Map.entry("dubai", "迪拜")
    );

    private CityNameTranslator() {
    }

    static String translate(String city) {
        if (city == null) {
            return "上海";
        }
        String normalized = city.trim();
        if (normalized.isBlank()) {
            return "上海";
        }
        String mapped = CITY_MAP.get(normalized.toLowerCase(Locale.ROOT));
        return mapped == null ? normalized : mapped;
    }
}
