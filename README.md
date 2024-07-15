# gmn

A CLI for generating things with Google Gemini API, built with Golang.

## Build

```bash
$ go install github.com/meinside/gmn@latest
```

## Configure

Create `config.json` file in `$XDG_CONFIG_HOME/gmn/` or `$HOME/.config/gmn/`:

```bash
$ mkdir -p ~/.config/gmn
$ $EDITOR ~/.config/gmn/config.json
```

with following content:

```json
{
  "google_ai_api_key": "ABCDEFGHIJK1234567890",

  "google_ai_model": "gemini-1.5-pro-latest",
  "system_instruction": "You are a chat bot which responds to user requests reliably and accurately.",

  "replace_http_urls_in_prompt_to_body_texts": false,
}
```

and replace things with your own values.

You can get your Google AI API key [here](https://aistudio.google.com/app/apikey).

### Using Infisical

You can use [Infisical](https://infisical.com/) for saving & retrieving your api key:

```json
{
  //"google_ai_api_key": "ABCDEFGHIJK1234567890",
  "infisical": {
    "client_id": "012345-abcdefg-987654321",
    "client_secret": "aAbBcCdDeEfFgG0123456789xyzwXYZW",

    "project_id": "012345abcdefg",
    "environment": "dev",
    "secret_type": "shared",

    "google_ai_api_key_key_path": "/path/to/your/KEY_TO_GOOGLE_AI_API_KEY",
  },

  "google_ai_model": "gemini-1.5-pro-latest",
  "system_instruction": "You are a chat bot which responds to user requests reliably and accurately.",

  "replace_http_urls_in_prompt_to_body_texts": false,
}
```


## Run

```bash
# help message
$ gmn -h

# generate with a text prompt
$ gmn -p "what is the answer to life, the universe, and everything?"

# generate with a text prompt and a file
$ gmn -p "summarize this csv file" -f "~/files/mydata.csv"
```

## License

MIT

