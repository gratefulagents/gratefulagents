def execute(data):
    if data["items"] > data["cap"] * 10:
        return {"work": 0}
    return {"work": data["items"]}
