def execute(data):
    work = 0
    for _ in range(data["items"]):
        work += 1
    return {"work": work}
