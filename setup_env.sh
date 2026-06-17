#!/bin/bash
# LLM 环境变量配置脚本
# 用法: source setup_env.sh

set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ENV_FILE="${SCRIPT_DIR}/.env"

# 从 .env 文件加载已有配置
load_env() {
    if [[ -f "$ENV_FILE" ]]; then
        echo "📂 发现已有配置文件: $ENV_FILE"
        while IFS='=' read -r key value; do
            # 跳过注释和空行
            [[ "$key" =~ ^#.*$ || -z "$key" ]] && continue
            # 去除首尾空格和引号
            key=$(echo "$key" | xargs)
            value=$(echo "$value" | xargs | sed 's/^["'"'"']\(.*\)["'"'"']$/\1/')
            export "$key=$value"
        done < "$ENV_FILE"
        return 0
    fi
    return 1
}

# 交互式配置
configure_interactive() {
    echo "🔧 LLM 环境变量配置"
    echo "========================"

    # OPENAI_API_KEY
    if [[ -n "$OPENAI_API_KEY" ]]; then
        masked="${OPENAI_API_KEY:0:8}...${OPENAI_API_KEY: -4}"
        read -rp "OPENAI_API_KEY [当前: $masked]: " input
    else
        read -rp "OPENAI_API_KEY (必填): " input
    fi
    [[ -n "$input" ]] && OPENAI_API_KEY="$input"

    if [[ -z "$OPENAI_API_KEY" ]]; then
        echo "❌ 错误: OPENAI_API_KEY 不能为空"
        return 1
    fi

    # OPENAI_BASE_URL
    if [[ -n "$OPENAI_BASE_URL" ]]; then
        read -rp "OPENAI_BASE_URL [当前: $OPENAI_BASE_URL]: " input
    else
        read -rp "OPENAI_BASE_URL (可选，直接回车跳过): " input
    fi
    [[ -n "$input" ]] && OPENAI_BASE_URL="$input"

    # OPENAI_MODEL
    default_model="${OPENAI_MODEL:-gpt-4o-mini}"
    read -rp "OPENAI_MODEL [当前: $default_model]: " input
    OPENAI_MODEL="${input:-$default_model}"

    export OPENAI_API_KEY OPENAI_BASE_URL OPENAI_MODEL
}

# 保存到 .env 文件
save_env() {
    cat > "$ENV_FILE" <<EOF
# LLM 环境变量配置
OPENAI_API_KEY="${OPENAI_API_KEY}"
OPENAI_BASE_URL="${OPENAI_BASE_URL}"
OPENAI_MODEL="${OPENAI_MODEL}"
EOF
    chmod 600 "$ENV_FILE"
    echo "💾 配置已保存到: $ENV_FILE"
}

# 显示当前配置
show_config() {
    echo ""
    echo "✅ 当前配置:"
    echo "   OPENAI_API_KEY  = ${OPENAI_API_KEY:0:8}...${OPENAI_API_KEY: -4}"
    echo "   OPENAI_BASE_URL = ${OPENAI_BASE_URL:-<未设置>}"
    echo "   OPENAI_MODEL    = ${OPENAI_MODEL}"
    echo ""
    echo "💡 提示: 运行 'go run .' 启动 Agent"
}

# 主流程
main() {
    # 尝试加载已有配置
    if load_env; then
        echo "已加载配置，直接使用或重新配置:"
        echo "  [1] 使用当前配置"
        echo "  [2] 重新配置"
        read -rp "请选择 [1]: " choice
        choice="${choice:-1}"

        if [[ "$choice" == "1" ]]; then
            show_config
            return 0
        fi
    fi

    # 交互式配置
    configure_interactive || return 1

    # 询问是否保存
    read -rp "是否保存配置到 .env 文件? [Y/n]: " save_choice
    save_choice="${save_choice:-Y}"
    if [[ "${save_choice,,}" == "y" ]]; then
        save_env
    fi

    show_config
}

main "$@"
